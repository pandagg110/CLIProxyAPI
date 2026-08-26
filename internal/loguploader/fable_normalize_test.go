package loguploader

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNormalizeFableRecordNativeCustomerShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fable.log")
	content := `=== REQUEST INFO ===
URL: /v1/messages
Timestamp: 2026-08-26T01:02:03+08:00

=== HEADERS ===
X-Claude-Code-Session-ID: session-1

=== REQUEST BODY ===
{"model":"claude-sonnet-5","output_config":{"effort":"xhigh"},"system":"system prompt","tools":[{"name":"Read","input_schema":{"type":"object"}}],"client_metadata":{"turn_id":"turn-1","thread_id":"thread-1","session_id":"session-1"},"messages":[{"role":"user","content":"hello"}]}

=== RESPONSE ===
Status: 200
{"id":"msg-1","type":"message","role":"assistant","content":[{"type":"thinking","thinking":"plan","signature":"sig"},{"type":"text","text":"done"}]}
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	source := sourceLog{
		Path: path, Relative: "alice/fable.log", KeyName: "alice", Model: "claude-sonnet-5",
		Provider: providerClaude, Timestamp: info.ModTime(), ModTime: info.ModTime(), Size: info.Size(),
	}
	record, hash, err := normalizeFableRecord(source)
	if err != nil {
		t.Fatal(err)
	}
	if hash == "" || record == nil {
		t.Fatalf("record=%#v hash=%q", record, hash)
	}
	if record.SessionID != "session-1" || record.Model != fableDeliveryModel || record.ThinkingEffort != "xhigh" {
		t.Fatalf("identity = %#v", record)
	}
	if string(record.System) != `[{"text":"system prompt","type":"text"}]` {
		t.Fatalf("system = %s", record.System)
	}
	if len(record.Tools) == 0 || len(record.Messages) != 2 || record.Messages[1].Role != "assistant" {
		t.Fatalf("record = %#v", record)
	}
	var decoded map[string]any
	var output bytes.Buffer
	written, _, err := writeFableNormalizedRecord(&output, source)
	if err != nil {
		t.Fatal(err)
	}
	if written != int64(output.Len()) || json.Unmarshal(bytes.TrimSpace(output.Bytes()), &decoded) != nil {
		t.Fatalf("invalid JSONL output: %s", output.Bytes())
	}
	if _, wrapped := decoded["raw_log"]; wrapped {
		t.Fatalf("Fable record was wrapped in raw_log: %s", output.Bytes())
	}
	if _, wrapped := decoded["header"]; wrapped {
		t.Fatalf("structured record still contains header: %s", output.Bytes())
	}
	request, ok := decoded["request"].(map[string]any)
	if !ok || request["body"] == nil {
		t.Fatalf("structured request = %#v", decoded["request"])
	}
	response, ok := decoded["response"].(map[string]any)
	if !ok || response["body"] == nil {
		t.Fatalf("structured response = %#v", decoded["response"])
	}
	metadata, ok := decoded["metadata"].(map[string]any)
	if !ok || metadata["source"] == nil || metadata["extra_info"] == nil {
		t.Fatalf("structured metadata = %#v", decoded["metadata"])
	}
	if metadata["message_id"] != "turn-1" || metadata["conversation_id"] != "thread-1" || metadata["session_id"] != "session-1" {
		t.Fatalf("structured identity = %#v", metadata)
	}
	if metadata["model"] != "claude-sonnet-5" || metadata["user_id"] != "alice" || metadata["think_type"] != "xhigh" {
		t.Fatalf("structured metadata identity = %#v", metadata)
	}
	if metadata["timestamp"] != "2026-08-25T17:02:03Z" {
		t.Fatalf("structured timestamp = %#v", metadata["timestamp"])
	}
}

func TestNormalizeFableRecordSSE(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fable.log")
	content := `=== REQUEST INFO ===
URL: /v1/messages
Timestamp: 2026-08-26T01:02:03+08:00

=== HEADERS ===
x-claude-code-session-id: session-sse

=== REQUEST BODY ===
{"model":"claude-fable-5","messages":[{"role":"user","content":"run"}]}

=== RESPONSE ===
event: message_start
data: {"type":"message_start","message":{"role":"assistant"}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool-1","name":"Read","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a.go\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_stop
data: {"type":"message_stop"}
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	source := sourceLog{Path: path, Relative: "alice/fable.log", KeyName: "alice", Model: "claude-fable-5", Provider: providerClaude, Timestamp: time.Now(), ModTime: info.ModTime(), Size: info.Size()}
	record, _, err := normalizeFableRecord(source)
	if err != nil {
		t.Fatal(err)
	}
	if record == nil || len(record.Messages) != 2 {
		t.Fatalf("record = %#v", record)
	}
	var blocks []map[string]any
	if err := json.Unmarshal(record.Messages[1].Content, &blocks); err != nil {
		t.Fatal(err)
	}
	if blocks[0]["type"] != "tool_use" || blocks[0]["name"] != "Read" {
		t.Fatalf("tool block = %#v", blocks[0])
	}
	input, ok := blocks[0]["input"].(map[string]any)
	if !ok || input["path"] != "a.go" {
		t.Fatalf("tool input = %#v", blocks[0]["input"])
	}
}

func TestNormalizeFableRecordFiltersSelectedHTTPErrorStatuses(t *testing.T) {
	for _, status := range []int{400, 401, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "fable.log")
			content := "=== REQUEST INFO ===\nURL: /v1/messages\nTimestamp: 2026-08-26T01:02:03+08:00\n\n=== REQUEST BODY ===\n" +
				`{"model":"claude-fable-5","messages":[{"role":"user","content":"hello"}]}` +
				fmt.Sprintf("\n\n=== RESPONSE ===\nStatus: %d\n{\"content\":[{\"type\":\"text\",\"text\":\"error\"}]}\n", status)
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			source := sourceLog{Path: path, Relative: "alice/fable.log", KeyName: "alice", Model: "claude-fable-5", Provider: providerClaude, Timestamp: info.ModTime(), ModTime: info.ModTime(), Size: info.Size()}
			record, _, err := normalizeFableRecord(source)
			if err != nil {
				t.Fatal(err)
			}
			if record != nil {
				t.Fatalf("status %d was not filtered: %#v", status, record)
			}
		})
	}
}

func TestNormalizeFableRecordFiltersAPIErrorResponseStatus(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fable.log")
	content := `=== REQUEST BODY ===
{"model":"claude-fable-5","messages":[{"role":"user","content":"hello"}]}

=== API ERROR RESPONSE ===
HTTP Status: 401
unauthorized

=== RESPONSE ===
Status: 200
{"content":[{"type":"text","text":"should not upload"}]}
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	source := sourceLog{Path: path, Relative: "alice/fable.log", KeyName: "alice", Model: "claude-fable-5", Provider: providerClaude, Timestamp: info.ModTime(), ModTime: info.ModTime(), Size: info.Size()}
	record, _, err := normalizeFableRecord(source)
	if err != nil {
		t.Fatal(err)
	}
	if record != nil {
		t.Fatalf("API error response was not filtered: %#v", record)
	}
}

func TestParseFableSSEDeduplicatesExactEventReplay(t *testing.T) {
	payload := "event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}` + "\n\n"
	raw, ok, err := parseFableSSE(payload)
	if err != nil || !ok {
		t.Fatalf("parseFableSSE() = ok=%v err=%v", ok, err)
	}
	var content []map[string]any
	if err := json.Unmarshal(raw, &content); err != nil {
		t.Fatal(err)
	}
	if len(content) != 1 || content[0]["text"] != "hello" {
		t.Fatalf("replayed SSE event was retained: %#v", content)
	}
}

func TestPrepareFableArchiveEntriesDropsDuplicateStreamingSources(t *testing.T) {
	dir := t.TempDir()
	content := `=== REQUEST INFO ===
URL: /v1/messages
Timestamp: 2026-08-26T01:02:03+08:00

=== HEADERS ===
x-claude-code-session-id: session-1

=== REQUEST BODY ===
{"model":"claude-fable-5","messages":[{"role":"user","content":"hello"}]}

=== RESPONSE ===
Status: 200
event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}
`
	sources := make([]sourceLog, 0, 2)
	for _, name := range []string{"first.log", "second.log"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, sourceLog{Path: path, Relative: "alice/" + name, KeyName: "alice", Model: "claude-fable-5", Provider: providerClaude, Timestamp: info.ModTime(), ModTime: info.ModTime(), Size: info.Size()})
	}
	entries, err := prepareFableArchiveEntries(sources)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("streaming duplicate entries = %d, want 1", len(entries))
	}
}

func TestBuildArchiveKeepsEachFableRequestRecord(t *testing.T) {
	dir := t.TempDir()
	workDir := filepath.Join(dir, "uploader")
	root := filepath.Join(dir, "logs")
	if err := os.MkdirAll(filepath.Join(root, "alice"), 0o750); err != nil {
		t.Fatal(err)
	}
	write := func(name, timestamp, text string) sourceLog {
		t.Helper()
		path := filepath.Join(root, "alice", name)
		body := `{"model":"claude-fable-5","messages":[{"role":"user","content":"` + text + `"}]}`
		content := "=== REQUEST INFO ===\nURL: /v1/messages\nTimestamp: " + timestamp + "\n\n=== HEADERS ===\nx-claude-code-session-id: session-1\n\n=== REQUEST BODY ===\n" + body + "\n\n=== RESPONSE ===\n" + `{"role":"assistant","content":[{"type":"text","text":"ok"}]}` + "\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := time.Parse(time.RFC3339, timestamp)
		if err != nil {
			t.Fatal(err)
		}
		return sourceLog{Path: path, Relative: "alice/" + name, KeyName: "alice", Model: "claude-fable-5", Provider: providerClaude, Timestamp: parsed, ArchiveHour: parsed.Truncate(time.Hour), Size: info.Size(), ModTime: info.ModTime()}
	}
	first := write("first.log", "2026-08-26T01:01:00+08:00", "first")
	second := write("second.log", "2026-08-26T01:02:00+08:00", "second")
	service := &Service{cfg: Config{WorkDir: workDir}, location: time.FixedZone("CST", 8*60*60)}
	archive, jsonlBytes, _, err := service.buildArchive(context.Background(), first.ArchiveHour, providerClaude, []sourceLog{first, second}, true)
	if err != nil {
		t.Fatal(err)
	}
	decompressed := readZstdFile(t, archive)
	if got := len(nonemptyLines(decompressed)); got != 2 {
		t.Fatalf("JSONL record count = %d, want 2: %s", got, decompressed)
	}
	for index, line := range nonemptyLines(decompressed) {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if _, ok := record["header"]; ok {
			t.Fatalf("record %d still contains header = %#v", index, record)
		}
		for _, field := range []string{
			"request", "response", "metadata",
		} {
			if _, ok := record[field]; !ok {
				t.Fatalf("record %d is missing %q: %#v", index, field, record)
			}
		}
		if metadata, ok := record["metadata"].(map[string]any); !ok || metadata["source"] == nil || metadata["extra_info"] == nil || metadata["session_id"] != "session-1" || metadata["model"] != "claude-fable-5" {
			t.Fatalf("record %d metadata = %#v", index, record["metadata"])
		}
	}
	var expectedJSONLBytes int64
	for _, line := range nonemptyLines(decompressed) {
		expectedJSONLBytes += int64(len(line) + 1)
	}
	if jsonlBytes != expectedJSONLBytes {
		t.Fatalf("jsonl bytes = %d, decompressed line bytes = %d", jsonlBytes, expectedJSONLBytes)
	}
}
