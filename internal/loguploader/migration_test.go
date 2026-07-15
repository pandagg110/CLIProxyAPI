package loguploader

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrateLegacyStateVerifiesAndBindsTarget(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 4, 10, 0, 0, location)
	hour := time.Date(2026, time.July, 15, 2, 0, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	archivesRoot := filepath.Join(t.TempDir(), "verified-archives")
	objectKey := "cliproxy-logs/2026/07/15/2026-07-15-02-all-models-3B.jsonl.zst"
	archivePath := filepath.Join(archivesRoot, "2026", "07", "15", filepath.Base(objectKey))
	if errMkdir := os.MkdirAll(filepath.Dir(archivePath), 0o750); errMkdir != nil {
		t.Fatalf("create verified archive directory: %v", errMkdir)
	}
	if errWrite := os.WriteFile(archivePath, []byte("abc"), 0o600); errWrite != nil {
		t.Fatalf("write verified archive: %v", errWrite)
	}
	archiveSHA256, archiveSize, errChecksum := fileSHA256(archivePath)
	if errChecksum != nil {
		t.Fatalf("checksum archive: %v", errChecksum)
	}
	if errMkdir := os.MkdirAll(workDir, 0o750); errMkdir != nil {
		t.Fatalf("create work directory: %v", errMkdir)
	}
	legacyState := map[string]any{
		"uploaded": map[string]any{},
		"objects": map[string]any{
			objectKey: map[string]any{"uploaded_at": now.Add(-time.Hour)},
		},
	}
	writeJSONFixture(t, filepath.Join(workDir, "state.json"), legacyState)
	audit := auditRecord{
		Timestamp:       now.Add(-time.Hour),
		Status:          "uploaded",
		Hour:            hour,
		SourceCount:     2,
		KeyNames:        map[string]auditKeyNameSummary{},
		JSONLBytes:      10,
		CompressedBytes: archiveSize,
		ObjectKey:       objectKey,
	}
	writeJSONFixture(t, filepath.Join(workDir, "audit.jsonl"), audit)
	manifestPath := filepath.Join(t.TempDir(), "manifest.jsonl")
	writeJSONFixture(t, manifestPath, legacyManifestEntry{
		Hour:            hour,
		ObjectKey:       objectKey,
		SHA256:          archiveSHA256,
		SourceCount:     2,
		JSONLBytes:      10,
		CompressedBytes: archiveSize,
	})

	uploader := &matchingFakeObjectUploader{fakeObjectUploader: &fakeObjectUploader{}, matches: true}
	cfg := testConfig(root, workDir)
	cfg.Upload.Enabled = true
	service := mustTestService(t, cfg, uploader, now)
	if errMigrate := service.MigrateLegacyState(context.Background(), manifestPath, archivesRoot, false); errMigrate != nil {
		t.Fatalf("migrate legacy state: %v", errMigrate)
	}
	if uploader.matchCalls != 1 || len(uploader.calls) != 0 {
		t.Fatalf("migration match calls=%d upload calls=%d", uploader.matchCalls, len(uploader.calls))
	}
	state, errState := service.loadState()
	if errState != nil {
		t.Fatalf("load migrated state: %v", errState)
	}
	if state.SchemaVersion != uploadStateSchemaVersion || state.Target.ID != service.target.ID {
		t.Fatalf("migrated schema/target = %d/%s", state.SchemaVersion, state.Target.ID)
	}
	if len(state.Hours) != 1 || len(state.Objects) != 1 || len(state.Uploaded) != 0 || len(state.PreparedHours) != 0 {
		t.Fatalf("migrated state hours=%d objects=%d uploaded=%d prepared=%d", len(state.Hours), len(state.Objects), len(state.Uploaded), len(state.PreparedHours))
	}
	backups, errGlob := filepath.Glob(filepath.Join(workDir, "state.json.legacy-*"))
	if errGlob != nil || len(backups) != 1 {
		t.Fatalf("legacy state backups = %v, err=%v", backups, errGlob)
	}
}

func writeJSONFixture(t *testing.T, path string, value any) {
	t.Helper()
	data, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		t.Fatalf("marshal JSON fixture: %v", errMarshal)
	}
	data = append(data, '\n')
	if errWrite := os.WriteFile(path, data, 0o600); errWrite != nil {
		t.Fatalf("write JSON fixture: %v", errWrite)
	}
}
