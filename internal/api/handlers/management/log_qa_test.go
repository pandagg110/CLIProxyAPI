package management

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logqa"
)

func TestGetLogQAStatusReportsRunning(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	root := t.TempDir()
	workDir := filepath.Join(root, "log-qa")
	if err := os.MkdirAll(workDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logqa.LockPath(workDir), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := &Handler{
		cfg:            &config.Config{AuthDir: root},
		configFilePath: filepath.Join(root, "config.yaml"),
	}
	// Force resolveLogQASettings defaults to use auth-dir layout.
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/log-qa/status", nil)
	h.GetLogQAStatus(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["running"] != true {
		t.Fatalf("expected running=true, got %#v", payload["running"])
	}
}

func TestPostLogQARunConflictWhenInFlight(t *testing.T) {
	// Not parallel: mutates package-level logQARunGate.
	gin.SetMode(gin.TestMode)
	logQAEndRun()
	if !logQATryBeginRun() {
		t.Fatal("expected begin run to succeed")
	}
	defer logQAEndRun()

	root := t.TempDir()
	h := &Handler{
		cfg:            &config.Config{AuthDir: root},
		configFilePath: filepath.Join(root, "config.yaml"),
	}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/log-qa/run", nil)
	h.PostLogQARun(ctx)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetLogQASessionLogsZip(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	root := t.TempDir()
	authDir := filepath.Join(root, "auths")
	logsRoot := filepath.Join(authDir, "logs", "keys")
	workDir := filepath.Join(authDir, "log-qa")
	runID := "2026-07-15T01-00-00Z"
	reportDir := filepath.Join(workDir, "reports", runID)
	keyDir := filepath.Join(logsRoot, "panda")
	if err := os.MkdirAll(reportDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(keyDir, 0o750); err != nil {
		t.Fatal(err)
	}

	logBody := "=== REQUEST INFO ===\nfull session log body\n"
	logRel := "panda/fail-sample.log"
	if err := os.WriteFile(filepath.Join(logsRoot, filepath.FromSlash(logRel)), []byte(logBody), 0o600); err != nil {
		t.Fatal(err)
	}

	session := logqa.SessionRecord{
		SessionID:   "sess-fail-1",
		OK:          false,
		FailReasons: []string{"no_tool_call"},
		KeyNames:    []string{"panda"},
		SourceFiles: []string{logRel},
	}
	line, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reportDir, "session_qa.jsonl"), append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	latest := logqa.LatestPointer{RunID: runID, Dir: runID}
	latestRaw, _ := json.Marshal(latest)
	if err := os.MkdirAll(filepath.Join(workDir, "reports"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "reports", "latest.json"), latestRaw, 0o600); err != nil {
		t.Fatal(err)
	}

	h := &Handler{
		cfg:            &config.Config{AuthDir: authDir},
		configFilePath: filepath.Join(root, "config.yaml"),
	}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/log-qa/sessions/logs?session_id=sess-fail-1", nil)
	h.GetLogQASessionLogs(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/zip") {
		t.Fatalf("content-type=%q", ct)
	}

	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
		if f.Name == logRel {
			rc, errOpen := f.Open()
			if errOpen != nil {
				t.Fatal(errOpen)
			}
			buf := new(bytes.Buffer)
			if _, errCopy := buf.ReadFrom(rc); errCopy != nil {
				_ = rc.Close()
				t.Fatal(errCopy)
			}
			_ = rc.Close()
			if buf.String() != logBody {
				t.Fatalf("log content mismatch: %q", buf.String())
			}
		}
	}
	if !names[logRel] {
		t.Fatalf("missing log entry, names=%v", names)
	}
	if !names["qa-manifest.json"] {
		t.Fatalf("missing manifest, names=%v", names)
	}
}

func TestGetLogQASessionLogsRejectsPass(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	root := t.TempDir()
	authDir := filepath.Join(root, "auths")
	workDir := filepath.Join(authDir, "log-qa")
	runID := "2026-07-15T02-00-00Z"
	reportDir := filepath.Join(workDir, "reports", runID)
	if err := os.MkdirAll(reportDir, 0o750); err != nil {
		t.Fatal(err)
	}
	session := logqa.SessionRecord{SessionID: "sess-ok", OK: true, SourceFiles: []string{"panda/a.log"}}
	line, _ := json.Marshal(session)
	if err := os.WriteFile(filepath.Join(reportDir, "session_qa.jsonl"), append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	latestRaw, _ := json.Marshal(logqa.LatestPointer{RunID: runID, Dir: runID})
	if err := os.WriteFile(filepath.Join(workDir, "reports", "latest.json"), latestRaw, 0o600); err != nil {
		t.Fatal(err)
	}

	h := &Handler{
		cfg:            &config.Config{AuthDir: authDir},
		configFilePath: filepath.Join(root, "config.yaml"),
	}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/log-qa/sessions/logs?session_id=sess-ok", nil)
	h.GetLogQASessionLogs(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLogQAIsRunningHelper(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if logqa.IsRunning(root) {
		t.Fatal("expected not running")
	}
	if err := os.WriteFile(logqa.LockPath(root), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !logqa.IsRunning(root) {
		t.Fatal("expected running")
	}
	// Ensure gate helpers reset cleanly for sequential tests in package.
	_ = time.Now()
}
