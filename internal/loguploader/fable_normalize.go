package loguploader

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	fableUpstreamSonnet5 = "claude-sonnet-5"
	fableDeliveryModel   = "claude-fable-5"
)

var fableErrorStatusRe = regexp.MustCompile(`(?mi)^HTTP Status:\s*(400|401|500|503)\b`)

// fableNormalizedRecord is the internal Fable request record used for response
// reconstruction before serialization. Each source request remains separate.
type fableNormalizedRecord struct {
	SessionID      string          `json:"session_id"`
	Model          string          `json:"model"`
	ThinkingEffort string          `json:"thinking_effort"`
	System         json.RawMessage `json:"system"`
	Tools          json.RawMessage `json:"tools"`
	Messages       []fableMessage  `json:"messages"`

	// These fields retain the source views needed by the common JSONL schema.
	// They are intentionally unexported so the legacy internal representation
	// is never emitted directly.
	source           sourceLog
	requestInfo      map[string]any
	headers          map[string]any
	requestBody      map[string]any
	responseEnvelope map[string]any
	responseContent  json.RawMessage
	responseID       string
	streaming        bool
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
	responseSection := firstFableResponseSection(text)
	if fableResponseStatusExcluded(responseSection) || fableErrorResponseStatusExcluded(text) {
		// Authentication, client, and transient upstream failures are not
		// conversation data and must not be published in the JSONL archive.
		return nil, hash, nil
	}

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

	response, ok, errResponse := parseFableResponse(responseSection)
	if errResponse != nil {
		return nil, hash, fmt.Errorf("parse fable response %s: %w", source.Relative, errResponse)
	}
	if !ok || len(response) == 0 {
		return nil, hash, nil
	}
	messages = append(messages, fableMessage{Role: "assistant", Content: response})
	requestInfo, _ := parsePairs(strings.Split(extractGateSection(text, "REQUEST INFO"), "\n"))
	headers, _ := parsePairs(strings.Split(extractGateSection(text, "HEADERS"), "\n"))
	return &fableNormalizedRecord{
		SessionID:        sessionID,
		Model:            model,
		ThinkingEffort:   effort,
		System:           system,
		Tools:            tools,
		Messages:         messages,
		source:           source,
		requestInfo:      requestInfo,
		headers:          headers,
		requestBody:      requestBody,
		responseEnvelope: fableResponseEnvelope(responseSection, response),
		responseContent:  response,
		responseID:       fableResponseID(responseSection),
		streaming:        strings.Contains(responseSection, "data:") || strings.Contains(responseSection, "event:"),
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
	structured := fableToStructuredRecord(record)
	if structured == nil {
		return 0, hash, nil
	}
	counter := &countingWriter{writer: dst}
	encoder := json.NewEncoder(counter)
	encoder.SetEscapeHTML(false)
	if errEncode := encoder.Encode(structured); errEncode != nil {
		return counter.count, hash, fmt.Errorf("encode structured fable record: %w", errEncode)
	}
	return counter.count, hash, nil
}

type fableStructuredRecord struct {
	Header   map[string]any `json:"header"`
	Request  map[string]any `json:"request"`
	Response map[string]any `json:"response"`
	Metadata map[string]any `json:"metadata"`
}

func fableToStructuredRecord(record *fableNormalizedRecord) *fableStructuredRecord {
	if record == nil {
		return nil
	}
	requestBody := record.requestBody
	clientMetadata, _ := requestBody["client_metadata"].(map[string]any)
	if clientMetadata == nil {
		clientMetadata = make(map[string]any)
	}
	turnMetadata := parseJSONObject(firstPresent(
		caseInsensitiveGet(clientMetadata, "x-codex-turn-metadata"),
		caseInsensitiveGetAny(record.headers, "x-codex-turn-metadata", "x-claude-turn-metadata"),
		caseInsensitiveGet(requestBody, "turn_metadata"),
	))

	messageID := firstPresent(
		caseInsensitiveGet(clientMetadata, "turn_id"),
		caseInsensitiveGet(turnMetadata, "turn_id"),
		caseInsensitiveGetAny(record.headers, "x-client-request-id"),
		record.responseID,
	)
	conversationID := firstPresent(
		caseInsensitiveGet(clientMetadata, "thread_id"),
		caseInsensitiveGet(turnMetadata, "thread_id"),
		caseInsensitiveGetAny(record.headers, "thread-id", "thread_id"),
		caseInsensitiveGet(clientMetadata, "conversation_id"),
	)
	sessionID := firstPresent(
		caseInsensitiveGet(clientMetadata, "session_id"),
		caseInsensitiveGet(turnMetadata, "session_id"),
		fableSessionIDFromHeadersAndBody(record.headers, requestBody),
		record.SessionID,
	)
	timestamp := timestampToUTC(caseInsensitiveGet(record.requestInfo, "timestamp"))
	if timestamp == nil || timestamp == "" {
		timestamp = record.source.Timestamp.UTC().Format(time.RFC3339Nano)
	}

	tools, inputs := fableToolsAndInputs(requestBody)
	toolResult := jsonValueOrEmptyArray(extractFableToolResults(record.responseContent))
	if body, ok := record.responseEnvelope["body"].(map[string]any); ok {
		if responseTools, exists := body["tools"]; exists {
			toolResult = jsonValueOrEmptyArray(responseTools)
		}
	}

	extraInfo := make(map[string]any)
	mergeFableExtraInfo(extraInfo, turnMetadata)
	mergeFableExtraInfo(extraInfo, clientMetadata)
	if metadata, ok := requestBody["metadata"].(map[string]any); ok {
		mergeFableExtraInfo(extraInfo, metadata)
	}

	return &fableStructuredRecord{
		Header: map[string]any{
			"schema_version":             1,
			"key_name":                   record.source.KeyName,
			"source_file":                record.source.Relative,
			"source_size_bytes":          record.source.Size,
			"sensitive_headers_redacted": true,
			"message_id":                 fableStringValue(messageID),
			"conversation_id":            fableStringValue(conversationID),
			"session_id":                 fableStringValue(sessionID),
			"think_type":                 fableThinkType(requestBody, record.ThinkingEffort),
			"timestamp":                  fableStringValue(timestamp),
			"model":                      firstPresent(mapGet(requestBody, "model"), record.source.Model),
			"model_name":                 firstPresent(mapGet(requestBody, "model"), record.source.Model),
			"user_id":                    record.source.KeyName,
		},
		Request: map[string]any{
			"info":    record.requestInfo,
			"headers": record.headers,
			"body":    requestBody,
		},
		Response: record.responseEnvelope,
		Metadata: map[string]any{
			"source": map[string]any{
				"source_file":       record.source.Relative,
				"source_size_bytes": record.source.Size,
				"timestamp":         record.source.Timestamp.Format(time.RFC3339Nano),
				"provider":          record.source.Provider,
			},
			"extra_info":  extraInfo,
			"tools":       jsonValueOrEmptyArray(tools),
			"inputs":      jsonValueOrEmptyArray(inputs),
			"tool_result": toolResult,
		},
	}
}

// fableToCommonRecord maps a Claude Messages snapshot into the same JSONL
// schema used by Codex/OpenAI logs. The Fable-specific parsing remains above;
// only the serialized contract is shared with the Codex path.
func fableToCommonRecord(record *fableNormalizedRecord) *codexNormalizedRecord {
	if record == nil {
		return nil
	}

	requestBody := record.requestBody
	clientMetadata, _ := requestBody["client_metadata"].(map[string]any)
	if clientMetadata == nil {
		clientMetadata = make(map[string]any)
	}
	turnMetadata := parseJSONObject(firstPresent(
		caseInsensitiveGet(clientMetadata, "x-codex-turn-metadata"),
		caseInsensitiveGetAny(record.headers, "x-codex-turn-metadata", "x-claude-turn-metadata"),
		caseInsensitiveGet(requestBody, "turn_metadata"),
	))

	messageID := firstPresent(
		caseInsensitiveGet(clientMetadata, "turn_id"),
		caseInsensitiveGet(turnMetadata, "turn_id"),
		caseInsensitiveGetAny(record.headers, "x-client-request-id"),
		record.responseID,
	)
	conversationID := firstPresent(
		caseInsensitiveGet(clientMetadata, "thread_id"),
		caseInsensitiveGet(turnMetadata, "thread_id"),
		caseInsensitiveGetAny(record.headers, "thread-id", "thread_id"),
		caseInsensitiveGet(clientMetadata, "conversation_id"),
	)
	sessionID := firstPresent(
		caseInsensitiveGet(clientMetadata, "session_id"),
		caseInsensitiveGet(turnMetadata, "session_id"),
		fableSessionIDFromHeadersAndBody(record.headers, requestBody),
		record.SessionID,
	)

	modelName := firstPresent(mapGet(requestBody, "model"), record.source.Model)
	thinkType := fableThinkType(requestBody, record.ThinkingEffort)
	timestamp := timestampToUTC(caseInsensitiveGet(record.requestInfo, "timestamp"))
	if timestamp == nil || timestamp == "" {
		timestamp = record.source.Timestamp.UTC().Format(time.RFC3339Nano)
	}

	tools, inputs := fableToolsAndInputs(requestBody)
	response := jsonValueOrEmptyArray(record.responseContent)
	toolResult := jsonValueOrEmptyArray(extractFableToolResults(record.responseContent))
	if body, ok := record.responseEnvelope["body"].(map[string]any); ok {
		if responseTools, exists := body["tools"]; exists {
			toolResult = jsonValueOrEmptyArray(responseTools)
		}
	}

	extraInfo := make(map[string]any)
	mergeFableExtraInfo(extraInfo, turnMetadata)
	mergeFableExtraInfo(extraInfo, clientMetadata)
	if metadata, ok := requestBody["metadata"].(map[string]any); ok {
		mergeFableExtraInfo(extraInfo, metadata)
	}

	metadata := map[string]any{
		"source": map[string]any{
			"source_file":       record.source.Relative,
			"source_size_bytes": record.source.Size,
			"timestamp":         record.source.Timestamp.Format(time.RFC3339Nano),
			"provider":          record.source.Provider,
		},
		"request": map[string]any{
			"info":    record.requestInfo,
			"headers": record.headers,
			"body":    requestBody,
		},
		"response": record.responseEnvelope,
	}

	return &codexNormalizedRecord{
		MessageID:      fableStringValue(messageID),
		ConversationID: fableStringValue(conversationID),
		SessionID:      fableStringValue(sessionID),
		ThinkType:      thinkType,
		ExtraInfo:      jsonStringField(extraInfo),
		Tools:          jsonStringField(tools),
		Inputs:         jsonStringField(inputs),
		Response:       jsonStringField(response),
		Timestamp:      fableStringValue(timestamp),
		ModelName:      fableStringValue(modelName),
		UserID:         record.source.KeyName,
		ToolResult:     jsonStringField(toolResult),
		Metadata:       jsonStringField(metadata),
	}
}

func fableStringValue(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func fableThinkType(body map[string]any, fallback string) any {
	if reasoning, ok := body["reasoning"].(map[string]any); ok {
		if effort := firstPresent(reasoning["effort"]); effort != nil {
			return effort
		}
	}
	if fallback != "" {
		return fallback
	}
	return "high"
}

func jsonValueOrEmptyArray(value any) any {
	if value == nil {
		return []any{}
	}
	if raw, ok := value.(json.RawMessage); ok {
		var decoded any
		if json.Unmarshal(raw, &decoded) == nil && decoded != nil {
			return decoded
		}
		return []any{}
	}
	if text, ok := value.(string); ok {
		var decoded any
		if json.Unmarshal([]byte(text), &decoded) == nil && decoded != nil {
			return decoded
		}
	}
	return value
}

func fableToolsAndInputs(body map[string]any) (any, any) {
	rawInputs := body["inputs"]
	if rawInputs == nil {
		rawInputs = body["input"]
	}
	if rawInputs == nil {
		rawInputs = body["messages"]
	}

	var additionalTools []any
	filteredInputs := rawInputs
	if inputList, ok := rawInputs.([]any); ok {
		filtered := make([]any, 0, len(inputList))
		for _, item := range inputList {
			object, ok := item.(map[string]any)
			if ok && object["type"] == "additional_tools" {
				if listed, ok := object["tools"].([]any); ok {
					additionalTools = append(additionalTools, listed...)
				}
				continue
			}
			filtered = append(filtered, item)
		}
		filteredInputs = filtered
	}

	tools := any(additionalTools)
	if len(additionalTools) == 0 {
		tools = body["tools"]
	}
	return jsonValueOrEmptyArray(tools), jsonValueOrEmptyArray(filteredInputs)
}

func mergeFableExtraInfo(target, values map[string]any) {
	for key, value := range values {
		switch strings.ToLower(key) {
		case "turn_id", "thread_id", "session_id", "conversation_id", "x-codex-turn-metadata":
			continue
		}
		if _, exists := target[key]; !exists {
			target[key] = value
		}
	}
}

func fableSessionIDFromHeadersAndBody(headers map[string]any, body map[string]any) string {
	for _, key := range []string{
		"x-claude-code-session-id", "x-session-id", "session-id",
		"x-stainless-session-id", "claude-session-id",
	} {
		if value := strings.TrimSpace(fmt.Sprint(caseInsensitiveGetAny(headers, key))); value != "" && value != "<nil>" {
			return value
		}
	}
	for _, value := range []any{
		caseInsensitiveGet(body, "session_id", "sessionId"),
		fableNestedValue(body, "metadata", "session_id"),
		fableNestedValue(body, "metadata", "sessionId"),
		caseInsensitiveGet(body, "client_metadata"),
	} {
		if sessionID := fableStringOrJSONField(value, "session_id"); sessionID != "" {
			return sessionID
		}
	}
	return ""
}

func extractFableToolResults(value any) []any {
	if raw, ok := value.(json.RawMessage); ok {
		var decoded any
		if json.Unmarshal(raw, &decoded) == nil {
			value = decoded
		}
	}
	return extractFableToolResultsValue(value)
}

func extractFableToolResultsValue(value any) []any {
	results := make([]any, 0)
	if object, ok := value.(map[string]any); ok {
		if content, exists := object["content"]; exists {
			results = append(results, extractFableToolResultsValue(content)...)
		}
		if tools, exists := object["tools"]; exists {
			results = append(results, extractFableToolResultsValue(tools)...)
		}
		if object["type"] == "tool_result" {
			results = append(results, object)
		}
		return results
	}
	items, _ := value.([]any)
	for _, item := range items {
		results = append(results, extractFableToolResultsValue(item)...)
	}
	return results
}

func fableResponseEnvelope(section string, content json.RawMessage) map[string]any {
	trimmed := strings.TrimSpace(section)
	lines := strings.Split(trimmed, "\n")
	start := payloadStartIndex(lines)
	prelude := strings.Join(lines[:start], "\n")
	payload := strings.Join(lines[start:], "\n")
	allPairs, _ := parsePairs(strings.Split(prelude, "\n"))
	info := make(map[string]any)
	headers := make(map[string]any)
	var status any
	for key, value := range allPairs {
		switch strings.ToLower(key) {
		case "status":
			status = parseStatusValue(value)
		case "timestamp":
			info[key] = value
		default:
			headers[key] = value
		}
	}
	if object, errDecode := decodeFableObject(payload); errDecode == nil {
		return map[string]any{
			"info":    info,
			"status":  status,
			"headers": headers,
			"body":    object,
		}
	}
	return map[string]any{
		"info":    info,
		"status":  status,
		"headers": headers,
		"body":    map[string]any{"content": jsonValueOrEmptyArray(content)},
		"stream":  map[string]any{"is_sse": true, "raw_sse": payload},
	}
}

func fableResponseID(section string) string {
	trimmed := strings.TrimSpace(section)
	lines := strings.Split(trimmed, "\n")
	start := payloadStartIndex(lines)
	payload := strings.Join(lines[start:], "\n")
	if object, errDecode := decodeFableObject(payload); errDecode == nil {
		if id, ok := object["id"].(string); ok && strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id)
		}
		if body, ok := object["body"].(map[string]any); ok {
			if id, ok := body["id"].(string); ok {
				return strings.TrimSpace(id)
			}
		}
	}
	for _, line := range strings.Split(trimmed, "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event) != nil {
			continue
		}
		if message, ok := event["message"].(map[string]any); ok {
			if id, ok := message["id"].(string); ok && strings.TrimSpace(id) != "" {
				return strings.TrimSpace(id)
			}
		}
		if id, ok := event["id"].(string); ok && strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id)
		}
	}
	return ""
}

type fableArchiveEntry struct {
	Index  int
	Record *fableNormalizedRecord
	Legacy bool
}

// prepareFableArchiveEntries converts every Fable request independently. A
// session can contain multiple request logs, and each request normally produces
// its own JSONL record even when the request body repeats the full message
// history. Exact duplicate streaming snapshots are omitted.
func prepareFableArchiveEntries(sources []sourceLog) ([]fableArchiveEntry, error) {
	entries := make([]fableArchiveEntry, 0, len(sources))
	seenStreaming := make(map[string]struct{})
	for index := range sources {
		record, hash, errNormalize := normalizeFableRecord(sources[index])
		if errNormalize != nil {
			if !fableRequestHasMessages(sources[index].Path) {
				sources[index].SHA256 = hashSourceFile(sources[index].Path)
				sources[index].JSONLBytes = 0
				entries = append(entries, fableArchiveEntry{Index: index, Legacy: true})
				continue
			}
			return nil, errNormalize
		}
		sources[index].SHA256 = hash
		sources[index].JSONLBytes = 0
		if record == nil {
			continue
		}
		if record.streaming {
			// Streaming reconnects can leave a second source file containing the
			// same completed response for the same conversation. Keep the first
			// copy while retaining both source fingerprints for accounting.
			requestBytes, _ := json.Marshal(record.requestBody)
			key := record.SessionID + "\n" +
				fmt.Sprintf("%x", sha256.Sum256(requestBytes)) + "\n" +
				fmt.Sprintf("%x", sha256.Sum256(record.responseContent))
			if _, duplicate := seenStreaming[key]; duplicate {
				continue
			}
			seenStreaming[key] = struct{}{}
		}
		entries = append(entries, fableArchiveEntry{Index: index, Record: record})
	}
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

func fableResponseStatusExcluded(section string) bool {
	status := fableResponseStatus(section)
	switch status {
	case 400, 401, 500, 503:
		return true
	default:
		return false
	}
}

func fableErrorResponseStatusExcluded(text string) bool {
	indices := gateSectionPattern.FindAllStringSubmatchIndex(text, -1)
	for index, location := range indices {
		name := strings.TrimSpace(text[location[2]:location[3]])
		if !strings.EqualFold(name, "API ERROR RESPONSE") {
			continue
		}
		end := len(text)
		if index+1 < len(indices) {
			end = indices[index+1][0]
		}
		if fableErrorStatusRe.MatchString(text[location[1]:end]) {
			return true
		}
	}
	return false
}

func fableResponseStatus(section string) int {
	trimmed := strings.TrimSpace(section)
	if trimmed == "" {
		return 0
	}
	lines := strings.Split(trimmed, "\n")
	start := payloadStartIndex(lines)
	if start > len(lines) {
		start = len(lines)
	}
	pairs, _ := parsePairs(lines[:start])
	for key, value := range pairs {
		if strings.EqualFold(strings.TrimSpace(key), "status") {
			if parsed, ok := parseStatusValue(value).(int); ok {
				return parsed
			}
		}
	}
	// Some clients write a JSON response object without a status prelude.
	payload := strings.Join(lines[start:], "\n")
	if object, errDecode := decodeFableObject(payload); errDecode == nil {
		if parsed, ok := parseStatusValue(object["status"]).(int); ok {
			return parsed
		}
		if response, ok := object["response"].(map[string]any); ok {
			if parsed, ok := parseStatusValue(response["status"]).(int); ok {
				return parsed
			}
		}
	}
	return 0
}

func parseFableSSE(payload string) (json.RawMessage, bool, error) {
	blocks := make(map[int]*fableResponseBlock)
	seenEvents := make(map[string]struct{})
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
		// A few transports replay an identical SSE event while reconnecting.
		// Ignore exact event replays so their deltas are not appended twice.
		if encoded, errEncode := json.Marshal(event); errEncode == nil {
			key := string(encoded)
			if _, exists := seenEvents[key]; exists {
				return nil
			}
			seenEvents[key] = struct{}{}
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
