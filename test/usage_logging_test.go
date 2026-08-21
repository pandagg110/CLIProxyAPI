package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestGeminiExecutorRecordsSuccessfulZeroUsageInQueue(t *testing.T) {
	model := fmt.Sprintf("gemini-2.5-flash-zero-usage-%d", time.Now().UnixNano())
	source := fmt.Sprintf("zero-usage-%d@example.com", time.Now().UnixNano())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/v1beta/models/" + model + ":generateContent"
		if r.URL.Path != wantPath {
			t.Fatalf("path = %q, want %q", r.URL.Path, wantPath)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":0,"totalTokenCount":0}}`))
	}))
	defer server.Close()

	executor := runtimeexecutor.NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gemini",
		Attributes: map[string]string{
			"api_key":  "test-upstream-key",
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"email": source,
		},
	}

	prevQueueEnabled := redisqueue.Enabled()
	prevUsageEnabled := redisqueue.UsageStatisticsEnabled()
	redisqueue.SetEnabled(false)
	redisqueue.SetEnabled(true)
	redisqueue.SetUsageStatisticsEnabled(true)
	t.Cleanup(func() {
		redisqueue.SetEnabled(false)
		redisqueue.SetEnabled(prevQueueEnabled)
		redisqueue.SetUsageStatisticsEnabled(prevUsageEnabled)
	})

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatGemini,
		OriginalRequest: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	waitForQueuedUsageModelTotalTokens(t, "gemini", model, 0)
}

func TestClaudeExecutorMergesStreamUsageInQueue(t *testing.T) {
	model := fmt.Sprintf("claude-stream-usage-%d", time.Now().UnixNano())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_usage\",\"model\":%q,\"usage\":{\"input_tokens\":2,\"cache_creation_input_tokens\":831,\"cache_read_input_tokens\":44225,\"output_tokens\":1}}}\n\n", model)
		_, _ = fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":244,\"output_tokens_details\":{\"thinking_tokens\":40}}}\n\n")
		_, _ = fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	executor := runtimeexecutor.NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "claude-auth-id",
		Index:    "claude-auth-index",
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "test-upstream-key",
			"base_url": server.URL,
		},
		Metadata: map[string]any{"account_uuid": "claude-account-uuid"},
	}

	prevQueueEnabled := redisqueue.Enabled()
	prevUsageEnabled := redisqueue.UsageStatisticsEnabled()
	redisqueue.SetEnabled(false)
	redisqueue.SetEnabled(true)
	redisqueue.SetUsageStatisticsEnabled(true)
	t.Cleanup(func() {
		redisqueue.SetEnabled(false)
		redisqueue.SetEnabled(prevQueueEnabled)
		redisqueue.SetUsageStatisticsEnabled(prevUsageEnabled)
	})

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, model)),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
	}

	got := waitForQueuedUsagePayload(t, "claude", model)
	if got.Source != "claude-account-uuid" || got.AuthID != "claude-auth-id" || got.AuthIndex != "claude-auth-index" {
		t.Fatalf("queued identity = source:%q auth_id:%q auth_index:%q", got.Source, got.AuthID, got.AuthIndex)
	}
	if got.Tokens.InputTokens != 2 || got.Tokens.OutputTokens != 244 || got.Tokens.ReasoningTokens != 40 || got.Tokens.CachedTokens != 44225 || got.Tokens.CacheReadTokens != 44225 || got.Tokens.CacheCreationTokens != 831 || got.Tokens.TotalTokens != 45302 {
		t.Fatalf("queued tokens = %+v", got.Tokens)
	}
}

func waitForQueuedUsageModelTotalTokens(t *testing.T, wantProvider, wantModel string, wantTokens int64) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		items := redisqueue.PopOldest(10)
		for _, item := range items {
			got, ok := parseQueuedUsagePayload(t, item)
			if !ok {
				continue
			}
			if got.Provider != wantProvider || got.Model != wantModel {
				continue
			}
			if got.Failed {
				t.Fatalf("payload failed = true, want false")
			}
			if got.Tokens.TotalTokens != wantTokens {
				t.Fatalf("payload total tokens = %d, want %d", got.Tokens.TotalTokens, wantTokens)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for queued usage payload for provider=%q model=%q", wantProvider, wantModel)
}

func waitForQueuedUsagePayload(t *testing.T, wantProvider, wantModel string) queuedUsagePayload {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	const settleWindow = 150 * time.Millisecond
	var first queuedUsagePayload
	var matchCount int
	var lastMatchAt time.Time
	for time.Now().Before(deadline) {
		now := time.Now()
		items := redisqueue.PopOldest(10)
		for _, item := range items {
			got, ok := parseQueuedUsagePayload(t, item)
			if ok && got.Provider == wantProvider && got.Model == wantModel {
				if got.Failed {
					t.Fatalf("payload failed = true, want false")
				}
				if matchCount == 0 {
					first = got
				}
				matchCount++
				lastMatchAt = now
			}
		}
		if matchCount > 1 {
			t.Fatalf("queued usage payload count = %d for provider=%q model=%q, want exactly 1", matchCount, wantProvider, wantModel)
		}
		if matchCount == 1 && !lastMatchAt.IsZero() && time.Since(lastMatchAt) >= settleWindow {
			return first
		}
		time.Sleep(10 * time.Millisecond)
	}
	if matchCount > 0 {
		t.Fatalf("queued usage payload did not settle after %d record(s) for provider=%q model=%q", matchCount, wantProvider, wantModel)
	}
	t.Fatalf("timed out waiting for queued usage payload for provider=%q model=%q", wantProvider, wantModel)
	return queuedUsagePayload{}
}

type queuedUsagePayload struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Source    string `json:"source"`
	AuthID    string `json:"auth_id"`
	AuthIndex string `json:"auth_index"`
	Failed    bool   `json:"failed"`
	Tokens    struct {
		InputTokens         int64 `json:"input_tokens"`
		OutputTokens        int64 `json:"output_tokens"`
		ReasoningTokens     int64 `json:"reasoning_tokens"`
		CachedTokens        int64 `json:"cached_tokens"`
		CacheReadTokens     int64 `json:"cache_read_tokens"`
		CacheCreationTokens int64 `json:"cache_creation_tokens"`
		TotalTokens         int64 `json:"total_tokens"`
	} `json:"tokens"`
}

func parseQueuedUsagePayload(t *testing.T, payload []byte) (queuedUsagePayload, bool) {
	t.Helper()

	var parsed queuedUsagePayload
	if len(payload) == 0 {
		return parsed, false
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return parsed, false
	}
	if parsed.Provider == "" || parsed.Model == "" {
		return parsed, false
	}
	return parsed, true
}
