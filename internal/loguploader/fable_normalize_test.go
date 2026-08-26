package loguploader

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
{"model":"claude-sonnet-5","output_config":{"effort":"xhigh"},"system":"system prompt","tools":[{"name":"Read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hello"}]}

=== RESPONSE ===
Status: 200
{"type":"message","role":"assistant","content":[{"type":"thinking","thinking":"plan","signature":"sig"},{"type":"text","text":"done"}]}
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

func TestBuildArchiveDeduplicatesFableSessionSnapshots(t *testing.T) {
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
	if got := len(nonemptyLines(decompressed)); got != 1 {
		t.Fatalf("JSONL record count = %d, want 1: %s", got, decompressed)
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(decompressed))), &record); err != nil {
		t.Fatal(err)
	}
	if record["session_id"] != "session-1" || record["model"] != "claude-fable-5" {
		t.Fatalf("record identity = %#v", record)
	}
	if jsonlBytes != int64(len(nonemptyLines(decompressed)[0])+1) {
		t.Fatalf("jsonl bytes = %d, decompressed line bytes = %d", jsonlBytes, len(nonemptyLines(decompressed)[0])+1)
	}
}
