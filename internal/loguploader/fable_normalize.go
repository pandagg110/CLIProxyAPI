package loguploader

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	fableUpstreamSonnet5 = "claude-sonnet-5"
	fableDeliveryModel   = "claude-fable-5"
)

// fableNormalizedRecord is the customer-facing six-field Claude session
// record. Nested values remain native JSON rather than JSON strings.
type fableNormalizedRecord struct {
	SessionID      string          `json:"session_id"`
	Model          string          `json:"model"`
	ThinkingEffort string          `json:"thinking_effort"`
	System         json.RawMessage `json:"system"`
	Tools          json.RawMessage `json:"tools"`
	Messages       []fableMessage  `json:"messages"`
}

type fableMessage struct {
	Content json.RawMessage `json:"content"`
	Role    string          `json:"role"`
}

type fableResponseBlock struct {
	Index       int
	Block       map[string]any
	Text        strings.Builder
	Thinking    strings.Builder
	Signature   strings.Builder
	PartialJSON strings.Builder
}

// normalizeFableRecord parses a Claude Messages API log into the customer
// six-field record. It returns a nil record when no assistant response was
// captured, allowing the archive builder to filter incomplete requests.
func normalizeFableRecord(source sourceLog) (*fableNormalizedRecord, string, error) {
	raw, errRead := os.ReadFile(source.Path)
	if errRead != nil {
		return nil, "", fmt.Errorf("read fable log for normalization: %w", errRead)
	}
	info, errStat := os.Stat(source.Path)
	if errStat != nil {
		return nil, "", fmt.Errorf("stat fable log after read: %w", errStat)
	}
	if info.Size() != source.Size || !info.ModTime().Equal(source.ModTime) {
		return nil, "", fmt.Errorf("fable log changed during normalization: %s", source.Relative)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(raw))
	text := string(raw)

	requestBody, errRequest := decodeFableObject(firstFableSection(text, "REQUEST BODY"))
	if errRequest != nil {
		return nil, hash, fmt.Errorf("decode fable request body %s: %w", source.Relative, errRequest)
	}

	sessionID := fableSessionID(text, requestBody)
	if sessionID == "" {
		// A real Claude Code request normally carries x-claude-code-session-id.
		// Keep a deterministic fallback for older logs so they remain uploadable.
		sessionID = source.Relative
	}
	model := firstFableString(requestBody["model"], source.Model)
	if strings.EqualFold(model, fableUpstreamSonnet5) {
		model = fableDeliveryModel
	}
	effort := fableThinkingEffort(requestBody)
	system := fableJSONArray(requestBody["system"])
	tools := fableJSONArray(requestBody["tools"])
	if _, hasMessages := requestBody["messages"]; !hasMessages {
		return nil, hash, fmt.Errorf("missing messages array")
	}
	messages, errMessages := fableRequestMessages(requestBody["messages"])
	if errMessages != nil {
		return nil, hash, fmt.Errorf("normalize fable messages %s: %w", source.Relative, errMessages)
	}

	response, ok, errResponse := parseFableResponse(firstFableResponseSection(text))
	if errResponse != nil {
		return nil, hash, fmt.Errorf("parse fable response %s: %w", source.Relative, errResponse)
	}
	if !ok || len(response) == 0 {
		return nil, hash, nil
	}
	messages = append(messages, fableMessage{Role: "assistant", Content: response})
	return &fableNormalizedRecord{
		SessionID:      sessionID,
		Model:          model,
		ThinkingEffort: effort,
		System:         system,
		Tools:          tools,
		Messages:       messages,
	}, hash, nil
}

func writeFableNormalizedRecord(dst interface{ Write([]byte) (int, error) }, source sourceLog) (int64, string, error) {
	record, hash, errNormalize := normalizeFableRecord(source)
	if errNormalize != nil {
		// Keep pre-session-format Claude logs uploadable. A valid Fable request
		// always has a messages array; only legacy/non-Claude-shaped records use
		// the redacted wrapper fallback.
		if !fableRequestHasMessages(source.Path) {
			return writeRawJSONLRecordWithHash(dst, source)
		}
		return 0, hash, errNormalize
	}
	if record == nil {
		return 0, hash, nil
	}
	return writeFableRecordValue(dst, record, hash)
}

func fableRequestHasMessages(path string) bool {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return false
	}
	body, errDecode := decodeFableObject(firstFableSection(string(raw), "REQUEST BODY"))
	if errDecode != nil {
		return false
	}
	_, ok := body["messages"]
	return ok
}

func writeFableRecordValue(dst interface{ Write([]byte) (int, error) }, record *fableNormalizedRecord, hash string) (int64, string, error) {
	counter := &countingWriter{writer: dst}
	encoder := json.NewEncoder(counter)
	encoder.SetEscapeHTML(false)
	if errEncode := encoder.Encode(record); errEncode != nil {
		return counter.count, hash, fmt.Errorf("encode normalized fable record: %w", errEncode)
	}
	return counter.count, hash, nil
}

type fableArchiveEntry struct {
	Index  int
	Record *fableNormalizedRecord
	Legacy bool
}

// prepareFableArchiveEntries keeps one complete conversation snapshot per
// session in an hourly archive. The latest request contains the full Claude
// message history, so older requests for the same session are represented by
// their source checksums but do not duplicate a JSONL record.
func prepareFableArchiveEntries(sources []sourceLog) ([]fableArchiveEntry, error) {
	selected := make(map[string]fableArchiveEntry)
	var legacy []fableArchiveEntry
	for index := range sources {
		record, hash, errNormalize := normalizeFableRecord(sources[index])
		if errNormalize != nil {
			if !fableRequestHasMessages(sources[index].Path) {
				sources[index].SHA256 = hashSourceFile(sources[index].Path)
				sources[index].JSONLBytes = 0
				legacy = append(legacy, fableArchiveEntry{Index: index, Legacy: true})
				continue
			}
			return nil, errNormalize
		}
		sources[index].SHA256 = hash
		sources[index].JSONLBytes = 0
		if record == nil {
			continue
		}
		key := sources[index].KeyName + "\n" + fableRecordSessionID(record)
		previous, exists := selected[key]
		if !exists || fableSourceSortLess(sources[previous.Index], sources[index]) {
			selected[key] = fableArchiveEntry{Index: index, Record: record}
		}
	}
	entries := make([]fableArchiveEntry, 0, len(selected))
	for _, entry := range selected {
		entries = append(entries, entry)
	}
	entries = append(entries, legacy...)
	sort.Slice(entries, func(i, j int) bool {
		return sources[entries[i].Index].Relative < sources[entries[j].Index].Relative
	})
	return entries, nil
}

func hashSourceFile(path string) string {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func firstFableSection(text, name string) string {
	for _, candidate := range []string{name, "API " + name, "API " + name + " 1"} {
		if section := extractGateSection(text, candidate); strings.TrimSpace(section) != "" {
			return section
		}
	}
	return ""
}

func firstFableResponseSection(text string) string {
	for _, candidate := range []string{"API RESPONSE", "API RESPONSE 1", "RESPONSE"} {
		if section := extractGateSection(text, candidate); strings.TrimSpace(section) != "" {
			return section
		}
	}
	return ""
}

func decodeFableObject(raw string) (map[string]any, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("missing REQUEST BODY section")
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(trimmed), &object); err == nil {
		return object, nil
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start >= 0 && end > start && json.Unmarshal([]byte(trimmed[start:end+1]), &object) == nil {
		return object, nil
	}
	return nil, fmt.Errorf("invalid JSON object")
}

func fableSessionID(text string, body map[string]any) string {
	headers := parseGateHeaders(text)
	for _, key := range []string{
		"x-claude-code-session-id", "x-session-id", "session-id",
		"x-stainless-session-id", "claude-session-id",
	} {
		if value := strings.TrimSpace(headers[strings.ToLower(key)]); value != "" {
			return value
		}
	}
	for _, value := range []any{
		body["session_id"], body["sessionId"],
		fableNestedValue(body, "metadata", "session_id"),
		fableNestedValue(body, "metadata", "sessionId"),
		fableNestedValue(body, "client_metadata", "session_id"),
	} {
		if sessionID := fableStringOrJSONField(value, "session_id"); sessionID != "" {
			return sessionID
		}
	}
	return ""
}

func fableNestedValue(object map[string]any, parent, child string) any {
	switch nested := object[parent].(type) {
	case map[string]any:
		return nested[child]
	case string:
		var decoded map[string]any
		if json.Unmarshal([]byte(nested), &decoded) == nil {
			return decoded[child]
		}
	}
	return nil
}

func fableStringOrJSONField(value any, key string) string {
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return ""
		}
		var object map[string]any
		if json.Unmarshal([]byte(trimmed), &object) == nil {
			return fableStringOrJSONField(object[key], key)
		}
		return trimmed
	default:
		if object, ok := value.(map[string]any); ok {
			if result, ok := object[key].(string); ok {
				return strings.TrimSpace(result)
			}
		}
	}
	return ""
}

func firstFableString(values ...any) string {
	for _, value := range values {
		if result, ok := value.(string); ok && strings.TrimSpace(result) != "" {
			return strings.TrimSpace(result)
		}
	}
	return "unknown"
}

func fableThinkingEffort(body map[string]any) string {
	values := []any{
		fableNestedValue(body, "output_config", "effort"),
		body["thinking_effort"],
		fableNestedValue(body, "metadata", "thinking_effort"),
	}
	for _, value := range values {
		if effort, ok := value.(string); ok && isFableEffort(effort) {
			return effort
		}
	}
	if thinking, ok := body["thinking"].(map[string]any); ok {
		if strings.EqualFold(firstFableString(thinking["type"]), "disabled") {
			return "low"
		}
	}
	return "high"
}

func isFableEffort(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func fableJSONArray(value any) json.RawMessage {
	if value == nil {
		return json.RawMessage("[]")
	}
	if text, ok := value.(string); ok {
		var decoded any
		if json.Unmarshal([]byte(text), &decoded) == nil {
			value = decoded
		} else if text != "" {
			value = []any{map[string]any{"type": "text", "text": text}}
		}
	}
	if _, ok := value.([]any); !ok {
		return json.RawMessage("[]")
	}
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return json.RawMessage("[]")
	}
	return raw
}

func fableRequestMessages(value any) ([]fableMessage, error) {
	items, ok := value.([]any)
	if !ok {
		return []fableMessage{}, nil
	}
	result := make([]fableMessage, 0, len(items))
	for index, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("messages[%d] is not an object", index)
		}
		role, _ := object["role"].(string)
		role = strings.TrimSpace(role)
		if role != "user" && role != "assistant" && role != "system" {
			return nil, fmt.Errorf("messages[%d] has invalid role %q", index, role)
		}
		content, ok := object["content"]
		if !ok {
			return nil, fmt.Errorf("messages[%d] has no content", index)
		}
		rawContent, errMarshal := json.Marshal(content)
		if errMarshal != nil {
			return nil, fmt.Errorf("messages[%d] content: %w", index, errMarshal)
		}
		if !isFableMessageContent(content) {
			return nil, fmt.Errorf("messages[%d] content must be a string or array", index)
		}
		result = append(result, fableMessage{Role: role, Content: rawContent})
	}
	return result, nil
}

func isFableMessageContent(value any) bool {
	switch value.(type) {
	case string, []any:
		return true
	default:
		return false
	}
}

func parseFableResponse(section string) (json.RawMessage, bool, error) {
	trimmed := strings.TrimSpace(section)
	if trimmed == "" {
		return nil, false, nil
	}
	if object, errDecode := decodeFableObject(trimmed); errDecode == nil {
		if content, ok := object["content"]; ok {
			raw, errMarshal := json.Marshal(content)
			return raw, errMarshal == nil && len(raw) > 0, errMarshal
		}
		if body, ok := object["body"].(map[string]any); ok {
			if content, exists := body["content"]; exists {
				raw, errMarshal := json.Marshal(content)
				return raw, errMarshal == nil && len(raw) > 0, errMarshal
			}
		}
	}
	// A regular HTTP response is wrapped by the logger with status and header
	// pairs before the JSON body. Extract the first complete JSON object in
	// that section when the wrapper is present.
	if start := strings.Index(trimmed, "{"); start >= 0 {
		if object, errDecode := decodeFableObject(trimmed[start:]); errDecode == nil {
			if content, ok := object["content"]; ok {
				raw, errMarshal := json.Marshal(content)
				return raw, errMarshal == nil && len(raw) > 0, errMarshal
			}
		}
	}
	return parseFableSSE(trimmed)
}

func parseFableSSE(payload string) (json.RawMessage, bool, error) {
	blocks := make(map[int]*fableResponseBlock)
	var currentEvent strings.Builder
	flush := func() error {
		data := strings.TrimSpace(currentEvent.String())
		currentEvent.Reset()
		if data == "" || data == "[DONE]" {
			return nil
		}
		var event map[string]any
		if errDecode := json.Unmarshal([]byte(data), &event); errDecode != nil {
			return nil
		}
		return applyFableSSEEvent(blocks, event)
	}
	for _, line := range strings.Split(payload, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if currentEvent.Len() > 0 {
				currentEvent.WriteByte('\n')
			}
			currentEvent.WriteString(data)
			continue
		}
		if strings.TrimSpace(line) == "" {
			if errFlush := flush(); errFlush != nil {
				return nil, false, errFlush
			}
		}
	}
	if errFlush := flush(); errFlush != nil {
		return nil, false, errFlush
	}
	indices := make([]int, 0, len(blocks))
	for index := range blocks {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	content := make([]map[string]any, 0, len(indices))
	for _, index := range indices {
		entry := blocks[index]
		block := cloneFableMap(entry.Block)
		typeName, _ := block["type"].(string)
		switch typeName {
		case "text":
			block["text"] = entry.Text.String()
		case "thinking":
			block["thinking"] = entry.Thinking.String()
			if entry.Signature.Len() > 0 {
				block["signature"] = entry.Signature.String()
			}
		case "tool_use":
			input := strings.TrimSpace(entry.PartialJSON.String())
			if input == "" {
				if _, exists := block["input"]; !exists {
					block["input"] = map[string]any{}
				}
			} else {
				var decoded any
				if json.Unmarshal([]byte(input), &decoded) != nil {
					return nil, false, fmt.Errorf("invalid tool input JSON for content block %d", index)
				}
				block["input"] = decoded
			}
		}
		content = append(content, block)
	}
	if len(content) == 0 {
		return nil, false, nil
	}
	raw, errMarshal := json.Marshal(content)
	return raw, errMarshal == nil, errMarshal
}

func applyFableSSEEvent(blocks map[int]*fableResponseBlock, event map[string]any) error {
	typeName, _ := event["type"].(string)
	index := fableNumber(event["index"])
	entry := blocks[index]
	if entry == nil {
		entry = &fableResponseBlock{Index: index, Block: map[string]any{}}
		blocks[index] = entry
	}
	switch typeName {
	case "content_block_start":
		if block, ok := event["content_block"].(map[string]any); ok {
			entry.Block = cloneFableMap(block)
		}
	case "content_block_delta":
		delta, _ := event["delta"].(map[string]any)
		deltaType, _ := delta["type"].(string)
		if _, exists := entry.Block["type"]; !exists {
			switch deltaType {
			case "text_delta":
				entry.Block["type"] = "text"
			case "thinking_delta", "signature_delta":
				entry.Block["type"] = "thinking"
			case "input_json_delta":
				entry.Block["type"] = "tool_use"
			}
		}
		switch deltaType {
		case "text_delta":
			if text, ok := delta["text"].(string); ok {
				entry.Text.WriteString(text)
			}
		case "thinking_delta":
			if text, ok := delta["thinking"].(string); ok {
				entry.Thinking.WriteString(text)
			}
		case "signature_delta":
			if signature, ok := delta["signature"].(string); ok {
				entry.Signature.WriteString(signature)
			}
		case "input_json_delta":
			if partial, ok := delta["partial_json"].(string); ok {
				entry.PartialJSON.WriteString(partial)
			}
		}
	}
	return nil
}

func fableNumber(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case json.Number:
		var result int
		_, _ = fmt.Sscanf(typed.String(), "%d", &result)
		return result
	default:
		return 0
	}
}

func cloneFableMap(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

func fableRecordSessionID(record *fableNormalizedRecord) string {
	if record == nil {
		return ""
	}
	return record.SessionID
}

func fableSourceSortLess(left, right sourceLog) bool {
	if !left.Timestamp.Equal(right.Timestamp) {
		return left.Timestamp.Before(right.Timestamp)
	}
	if !left.ModTime.Equal(right.ModTime) {
		return left.ModTime.Before(right.ModTime)
	}
	return left.Relative < right.Relative
}

func fableTimestampOrModTime(source sourceLog) time.Time {
	if !source.Timestamp.IsZero() {
		return source.Timestamp
	}
	return source.ModTime
}
