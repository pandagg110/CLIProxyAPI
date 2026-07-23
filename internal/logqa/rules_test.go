package logqa

import (
	"encoding/json"
	"testing"
	"time"
)

func TestScoreInputExcludesIDEAndEnvCountsTools(t *testing.T) {
	t.Parallel()
	rules := RulesConfig{
		MinPromptRounds:          4,
		RequireToolCall:          true,
		RejectDuplicateAssistant: true,
		ExcludeIDEContext:        true,
		ExcludeEnvContext:        true,
		ExcludeTitleSummary:      true,
	}
	input := []any{
		map[string]any{"type": "message", "role": "user", "content": "# Context from my IDE setup:\nfoo"},
		map[string]any{"type": "message", "role": "user", "content": "<environment_context>\n</environment_context>"},
		map[string]any{"type": "message", "role": "user", "content": "real question one"},
		map[string]any{"type": "custom_tool_call", "name": "exec", "arguments": "{}"},
		map[string]any{"type": "message", "role": "assistant", "content": "hello"},
		map[string]any{"type": "message", "role": "assistant", "content": "hello"},
	}
	m := scoreInput(input, "turn", rules)
	if m.PromptRounds != 1 {
		t.Fatalf("prompt rounds = %d, want 1", m.PromptRounds)
	}
	if m.ToolCalls < 1 {
		t.Fatalf("tool calls = %d, want >=1", m.ToolCalls)
	}
	if m.DupAssistant != 1 {
		t.Fatalf("dup assistant groups = %d, want 1", m.DupAssistant)
	}
	ok, reasons := EvaluateSession(m.PromptRounds, m.ToolCalls, m.DupAssistant, rules)
	if ok {
		t.Fatalf("expected fail, reasons=%v", reasons)
	}
}

func TestEvaluateSessionPass(t *testing.T) {
	t.Parallel()
	rules := RulesConfig{
		MinPromptRounds:          4,
		RequireToolCall:          true,
		RejectDuplicateAssistant: true,
	}
	ok, reasons := EvaluateSession(4, 2, 0, rules)
	if !ok {
		t.Fatalf("expected pass, reasons=%v", reasons)
	}
}

func TestAggregateSessionsMaxInputSnapshot(t *testing.T) {
	t.Parallel()
	rules := RulesConfig{MinPromptRounds: 4, RequireToolCall: true, RejectDuplicateAssistant: true}
	requests := []RequestRecord{
		{SessionID: "s1", ThreadID: "t1", SourceFile: "a/1.log", InputLen: 10, PromptRounds: 1, ToolCalls: 5},
		{SessionID: "s1", ThreadID: "t1", SourceFile: "a/2.log", InputLen: 50, PromptRounds: 4, ToolCalls: 8},
		{SessionID: "s2", ThreadID: "t2", SourceFile: "b/1.log", InputLen: 3, PromptRounds: 1, ToolCalls: 0},
	}
	sessions := AggregateSessions(requests, rules)
	if len(sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(sessions))
	}
	var s1 SessionRecord
	for _, s := range sessions {
		if s.SessionID == "s1" {
			s1 = s
		}
	}
	if s1.PromptRounds != 4 || s1.ToolCalls != 8 || !s1.OK {
		raw, _ := json.Marshal(s1)
		t.Fatalf("unexpected s1: %s", raw)
	}
}

func TestPickSnapshotSkipsCompactionWhenNormalExists(t *testing.T) {
	t.Parallel()
	rules := RulesConfig{MinPromptRounds: 4, RequireToolCall: true, RejectDuplicateAssistant: true}
	requests := []RequestRecord{
		{
			SessionID: "sess-compact", ThreadID: "t1", SourceFile: "k/normal.log",
			RequestKind: "turn", InputLen: 40, PromptRounds: 5, ToolCalls: 10,
		},
		{
			// Compaction is longer (typical) but must not win snapshot selection.
			SessionID: "sess-compact", ThreadID: "t1", SourceFile: "k/compact.log",
			RequestKind: "compaction", InputLen: 900, PromptRounds: 0, ToolCalls: 592,
		},
	}
	sessions := AggregateSessions(requests, rules)
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	s := sessions[0]
	if s.PromptRounds != 5 || s.ToolCalls != 10 || !s.OK {
		raw, _ := json.Marshal(s)
		t.Fatalf("expected normal-turn snapshot, got %s", raw)
	}
	if s.RequestCount != 2 {
		t.Fatalf("request_count=%d, want 2 (compaction still listed in session files)", s.RequestCount)
	}
}

func TestPickSnapshotFallbackToCompactionOnly(t *testing.T) {
	t.Parallel()
	// When only compaction logs remain, still produce a snapshot (last resort).
	got := pickSnapshot([]RequestRecord{
		{SourceFile: "k/c1.log", RequestKind: "compaction", InputLen: 100, PromptRounds: 0, ToolCalls: 50},
		{SourceFile: "k/c2.log", RequestKind: "compaction", InputLen: 200, PromptRounds: 0, ToolCalls: 90},
	})
	if got.SourceFile != "k/c2.log" || got.ToolCalls != 90 {
		t.Fatalf("unexpected fallback snapshot: %+v", got)
	}
}

func TestNewRunIDUsesLocationWallClock(t *testing.T) {
	t.Parallel()
	loc := time.FixedZone("CST", 8*3600)
	// 2026-07-23 17:56:55 +08:00
	ts := time.Date(2026, 7, 23, 17, 56, 55, 0, loc)
	got := newRunID(ts)
	want := "2026-07-23T17-56-55+0800"
	if got != want {
		t.Fatalf("newRunID = %q, want %q", got, want)
	}
	// Must not force UTC (legacy form …Z at 09:56).
	if got == "2026-07-23T09-56-55Z" {
		t.Fatal("run id unexpectedly forced to UTC")
	}
}

func TestTitleSummaryExcluded(t *testing.T) {
	t.Parallel()
	rules := RulesConfig{ExcludeTitleSummary: true}
	if !isTitleOrSummary("Please generate a title for this chat", "turn") {
		t.Fatal("expected title detection")
	}
	if !isRealUserPrompt("message", "user", "normal ask", "turn", rules) {
		t.Fatal("normal prompt should count")
	}
	if isRealUserPrompt("message", "user", "Please generate a title for this chat", "turn", rules) {
		t.Fatal("title prompt should be excluded")
	}
}
