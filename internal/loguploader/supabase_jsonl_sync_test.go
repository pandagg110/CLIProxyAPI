package loguploader

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncSupabaseJSONLDryRunReportsExactJSONL(t *testing.T) {
	service, state, record := newSupabaseHistoryTestFixture(t, "https://one.supabase.co/functions/v1/ingest")
	record.ObjectKey = "cliproxy-logs/2026/07/18/2026-07-18-01-codex56sol-150B.jsonl.zst"
	record.JSONLBytes = 150
	record.KeyNames["alice"] = auditKeyNameSummary{
		SourceCount: 1,
		SourceBytes: 100,
		JSONLBytes:  150,
		Models: map[string]auditModelSummary{
			"gpt-5.6-sol": {SourceCount: 1, SourceBytes: 100, JSONLBytes: 150},
		},
	}
	object := state.Objects["cliproxy-logs/2026/07/18/archive"]
	delete(state.Objects, "cliproxy-logs/2026/07/18/archive")
	object.ObjectKey = record.ObjectKey
	state.Objects[record.ObjectKey] = object
	hour := state.Hours[hourStateKey(record.Hour, record.Provider)]
	hour.ObjectKey = record.ObjectKey
	state.Hours[hourStateKey(record.Hour, record.Provider)] = hour
	writeHistoryAuditFile(t, filepath.Join(service.cfg.WorkDir, "audit.jsonl"), record)
	if errSave := service.saveState(state); errSave != nil {
		t.Fatalf("save initial state: %v", errSave)
	}

	summary, errSync := service.SyncSupabaseJSONL(context.Background(), true)
	if errSync != nil {
		t.Fatalf("dry-run JSONL sync: %v", errSync)
	}
	if summary.Pending != 1 || summary.LiveManaged != 0 || summary.JSONLBytes != 150 || summary.FilenameMismatches != 0 {
		t.Fatalf("dry-run summary = %#v", summary)
	}
}

func TestSyncSupabaseJSONLSkipsLiveManaged(t *testing.T) {
	service, state, record := newSupabaseHistoryTestFixture(t, "https://one.supabase.co/functions/v1/ingest")
	eventID := "cliproxy-v1." + strings.Repeat("d", 64)
	record.SupabaseEventID = eventID
	state = withUploadedHourSupabaseEventID(t, state, hourStateKey(record.Hour, record.Provider), eventID)
	writeHistoryAuditFile(t, filepath.Join(service.cfg.WorkDir, "audit.jsonl"), record)
	if errSave := service.saveState(state); errSave != nil {
		t.Fatalf("save initial state: %v", errSave)
	}

	summary, errSync := service.SyncSupabaseJSONL(context.Background(), true)
	if errSync != nil {
		t.Fatalf("live-managed JSONL sync: %v", errSync)
	}
	if summary.LiveManaged != 1 || summary.Pending != 0 {
		t.Fatalf("live-managed summary = %#v", summary)
	}
}

func TestSyncSupabaseJSONLPostsExactPerKeyJSONL(t *testing.T) {
	service, state, record := newSupabaseHistoryTestFixture(t, "https://one.supabase.co/functions/v1/ingest")
	writeHistoryAuditFile(t, filepath.Join(service.cfg.WorkDir, "audit.jsonl"), record)
	if errSave := service.saveState(state); errSave != nil {
		t.Fatalf("save initial state: %v", errSave)
	}
	t.Setenv(service.cfg.Supabase.IngestTokenEnv, "history-token")
	requests := 0
	service.supabaseHTTPDoer = httpDoerFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		raw, errRead := io.ReadAll(request.Body)
		if errRead != nil {
			t.Fatalf("read JSONL sync body: %v", errRead)
		}
		var payload supabaseEventPayload
		if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil {
			t.Fatalf("decode JSONL sync payload: %v", errUnmarshal)
		}
		if payload.UsagePrecision == supabaseUsagePrecisionBatchOnly || len(payload.Usage) != 1 || payload.Usage[0].JSONLBytes == nil {
			t.Fatalf("JSONL sync payload = %#v", payload)
		}
		if *payload.Usage[0].JSONLBytes != record.JSONLBytes {
			t.Fatalf("per-key jsonl = %d, want %d", *payload.Usage[0].JSONLBytes, record.JSONLBytes)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"status":"inserted","event_id":"` + payload.EventID + `"}`)),
			Header:     make(http.Header),
		}, nil
	})

	summary, errSync := service.SyncSupabaseJSONL(context.Background(), false)
	if errSync != nil {
		t.Fatalf("JSONL sync: %v", errSync)
	}
	if requests != 1 || summary.Inserted != 1 || summary.Checkpointed != 1 {
		t.Fatalf("JSONL sync summary = %#v requests=%d", summary, requests)
	}
	after, errLoad := service.loadState()
	if errLoad != nil {
		t.Fatalf("load state: %v", errLoad)
	}
	if len(after.SupabaseJSONLSync) != 1 {
		t.Fatalf("jsonl checkpoints = %#v", after.SupabaseJSONLSync)
	}

	second, errSecond := service.SyncSupabaseJSONL(context.Background(), false)
	if errSecond != nil {
		t.Fatalf("repeat JSONL sync: %v", errSecond)
	}
	if requests != 1 || second.AlreadyCheckpointed != 1 || second.Pending != 0 {
		t.Fatalf("repeat summary = %#v requests=%d", second, requests)
	}
}
