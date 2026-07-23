package logqa

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func reportsDir(workDir string) string {
	return filepath.Join(workDir, "reports")
}

func writeRunReport(workDir string, summary RunSummary, sessions []SessionRecord, requests []RequestRecord, keepRuns int) error {
	if err := os.MkdirAll(reportsDir(workDir), 0o750); err != nil {
		return fmt.Errorf("create reports dir: %w", err)
	}

	runDir := filepath.Join(reportsDir(workDir), summary.RunID)
	tmpDir := runDir + ".tmp"
	_ = os.RemoveAll(tmpDir)
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		return fmt.Errorf("create tmp report dir: %w", err)
	}

	if err := writeJSON(filepath.Join(tmpDir, "summary.json"), summary); err != nil {
		return err
	}
	if err := writeJSONL(filepath.Join(tmpDir, "session_qa.jsonl"), sessions); err != nil {
		return err
	}
	fails := make([]SessionRecord, 0)
	for _, s := range sessions {
		if !s.OK {
			fails = append(fails, s)
		}
	}
	if err := writeJSONL(filepath.Join(tmpDir, "fail_sessions.jsonl"), fails); err != nil {
		return err
	}
	if err := writeJSONL(filepath.Join(tmpDir, "request_qa.jsonl"), requests); err != nil {
		return err
	}

	_ = os.RemoveAll(runDir)
	if err := os.Rename(tmpDir, runDir); err != nil {
		return fmt.Errorf("publish report dir: %w", err)
	}

	latest := LatestPointer{RunID: summary.RunID, Dir: summary.RunID}
	if err := writeJSON(filepath.Join(reportsDir(workDir), "latest.json"), latest); err != nil {
		return err
	}
	return gcOldReports(workDir, keepRuns)
}

func writeJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", filepath.Base(path), err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s: %w", filepath.Base(path), err)
	}
	return nil
}

func writeJSONL[T any](path string, rows []T) error {
	var b strings.Builder
	enc := json.NewEncoder(&nopWriter{&b})
	enc.SetEscapeHTML(false)
	for _, row := range rows {
		if err := enc.Encode(row); err != nil {
			return fmt.Errorf("encode jsonl %s: %w", filepath.Base(path), err)
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("write jsonl %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename jsonl %s: %w", filepath.Base(path), err)
	}
	return nil
}

type nopWriter struct{ b *strings.Builder }

func (w *nopWriter) Write(p []byte) (int, error) { return w.b.Write(p) }

func gcOldReports(workDir string, keepRuns int) error {
	if keepRuns <= 0 {
		return nil
	}
	entries, err := os.ReadDir(reportsDir(workDir))
	if err != nil {
		return err
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasSuffix(e.Name(), ".tmp") {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)
	if len(dirs) <= keepRuns {
		return nil
	}
	for _, name := range dirs[:len(dirs)-keepRuns] {
		_ = os.RemoveAll(filepath.Join(reportsDir(workDir), name))
	}
	return nil
}

// newRunID builds a filesystem-safe run directory name in the wall-clock of t.
// Callers should pass a time already located in the configured timezone
// (default Asia/Shanghai). Example: 2026-07-23T17-56-55+0800
func newRunID(t time.Time) string {
	// -0700 is Go's reference layout for numeric zone offsets; for CST it yields +0800.
	// Avoid ":" so the id remains a single path segment on all OSes.
	return t.Format("2006-01-02T15-04-05-0700")
}

// ReadLatestPointer loads reports/latest.json if present.
func ReadLatestPointer(workDir string) (LatestPointer, error) {
	raw, err := os.ReadFile(filepath.Join(reportsDir(workDir), "latest.json"))
	if err != nil {
		return LatestPointer{}, err
	}
	var p LatestPointer
	if err := json.Unmarshal(raw, &p); err != nil {
		return LatestPointer{}, err
	}
	return p, nil
}

// ReadSummary loads a summary.json for a run (or latest).
func ReadSummary(workDir, runID string) (RunSummary, error) {
	if runID == "" {
		p, err := ReadLatestPointer(workDir)
		if err != nil {
			return RunSummary{}, err
		}
		runID = p.Dir
		if runID == "" {
			runID = p.RunID
		}
	}
	if err := validateRunID(runID); err != nil {
		return RunSummary{}, err
	}
	raw, err := os.ReadFile(filepath.Join(reportsDir(workDir), runID, "summary.json"))
	if err != nil {
		return RunSummary{}, err
	}
	var s RunSummary
	if err := json.Unmarshal(raw, &s); err != nil {
		return RunSummary{}, err
	}
	return s, nil
}

func validateRunID(runID string) error {
	if runID == "" || strings.Contains(runID, "..") || strings.ContainsAny(runID, `/\`) {
		return fmt.Errorf("invalid run_id")
	}
	return nil
}
