package loguploader

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const legacyAllModelsPolicyNaming = "all-models-jsonl-size-v1"

func TestValidateUploadStateMigratesLegacyV2NamingPolicy(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 5, 10, 0, 0, location)
	service := mustTestService(t, testConfig(filepath.Join(t.TempDir(), "keys"), filepath.Join(t.TempDir(), "uploader")), nil, now)
	state, legacyObjectKey, hourKey := legacyAllModelsV2State(service)

	if errValidate := service.validateUploadState(state); errValidate != nil {
		t.Fatalf("validate legacy v2 naming policy: %v", errValidate)
	}
	assertLegacyNamingStateMigrated(t, service, state, legacyObjectKey, hourKey)
}

func TestLoadStateMigratesLegacyV2NamingPolicy(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 5, 10, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	service := mustTestService(t, testConfig(root, workDir), nil, now)
	state, legacyObjectKey, hourKey := legacyAllModelsV2State(service)
	if errMkdir := os.MkdirAll(workDir, 0o750); errMkdir != nil {
		t.Fatalf("create work directory: %v", errMkdir)
	}
	writeJSONFixture(t, service.statePath(), state)

	loaded, errLoad := service.loadState()
	if errLoad != nil {
		t.Fatalf("load legacy v2 naming policy: %v", errLoad)
	}
	assertLegacyNamingStateMigrated(t, service, &loaded, legacyObjectKey, hourKey)
}

func TestValidateUploadStateStillRejectsOtherPolicyMismatches(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 5, 10, 0, 0, location)
	service := mustTestService(t, testConfig(filepath.Join(t.TempDir(), "keys"), filepath.Join(t.TempDir(), "uploader")), nil, now)
	tests := []struct {
		name   string
		mutate func(policy *uploadPolicy)
	}{
		{
			name: "timezone differs",
			mutate: func(policy *uploadPolicy) {
				policy.Timezone = "UTC"
			},
		},
		{
			name: "grouping differs",
			mutate: func(policy *uploadPolicy) {
				policy.Grouping = "request-timestamp-hour-v0"
			},
		},
		{
			name: "unknown naming differs",
			mutate: func(policy *uploadPolicy) {
				policy.Naming = "unknown-jsonl-size-v9"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, _, _ := legacyAllModelsV2State(service)
			state.Policy = service.policy
			test.mutate(&state.Policy)
			errValidate := service.validateUploadState(state)
			if errValidate == nil || !strings.Contains(errValidate.Error(), "upload state policy mismatch") {
				t.Fatalf("validateUploadState error = %v, want policy mismatch", errValidate)
			}
			if state.dirty {
				t.Fatal("rejected policy mismatch unexpectedly marked state dirty")
			}
		})
	}
}

func TestRepeatedDryRunRemovesLegacyAllModelsArchive(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 3, 10, 0, 0, location)
	hour := time.Date(2026, time.July, 15, 1, 0, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	mustWriteLog(t, root, "panda", "source.log", requestLog(hour.Add(10*time.Minute), "model-a", "source"), hour.Add(11*time.Minute))
	service := mustTestService(t, testConfig(root, workDir), nil, now)
	service.cfg.Upload.Enabled = true

	if errRun := service.RunOnce(context.Background(), true); errRun != nil {
		t.Fatalf("first dry run: %v", errRun)
	}
	archives := findFilesWithSuffix(t, filepath.Join(workDir, "dry-run-archives"), ".jsonl.zst")
	if len(archives) != 1 || !strings.Contains(filepath.Base(archives[0]), "-codex56sol-") {
		t.Fatalf("first dry-run archives = %v, want one codex56sol archive", archives)
	}
	legacyPath := filepath.Join(filepath.Dir(archives[0]), hour.Format("2006-01-02-15")+"-all-models-999B.jsonl.zst")
	if errWrite := os.WriteFile(legacyPath, []byte("legacy dry-run archive"), 0o640); errWrite != nil {
		t.Fatalf("write legacy dry-run archive: %v", errWrite)
	}

	if errRun := service.RunOnce(context.Background(), true); errRun != nil {
		t.Fatalf("second dry run: %v", errRun)
	}
	if _, errStat := os.Stat(legacyPath); !os.IsNotExist(errStat) {
		t.Fatalf("legacy all-models archive was not removed: %v", errStat)
	}
	archives = findFilesWithSuffix(t, filepath.Join(workDir, "dry-run-archives"), ".jsonl.zst")
	if len(archives) != 1 || !strings.Contains(filepath.Base(archives[0]), "-codex56sol-") {
		t.Fatalf("final dry-run archives = %v, want only one codex56sol archive", archives)
	}
}

func legacyAllModelsV2State(service *Service) (*uploadState, string, string) {
	state := service.newUploadState()
	state.Policy.Naming = legacyAllModelsPolicyNaming
	hour := time.Date(2026, time.July, 15, 1, 0, 0, 0, service.location)
	hourKey := hourStateKey(hour)
	objectKey := "cliproxy-logs/2026/07/15/2026-07-15-01-all-models-1K.jsonl.zst"
	archiveSHA := strings.Repeat("a", 64)
	state.Objects[objectKey] = uploadedObject{
		ObjectKey:      objectKey,
		CompressedSize: 512,
		ArchiveSHA256:  archiveSHA,
		Verification:   "remote-head-match",
		UploadedAt:     hour.Add(time.Hour),
		VerifiedAt:     hour.Add(time.Hour),
	}
	state.Hours[hourKey] = uploadedHour{
		Status:         "sealed",
		ObjectKey:      objectKey,
		ArchiveSHA256:  archiveSHA,
		ManifestSHA256: "legacy-verified:" + archiveSHA,
		UploadedAt:     hour.Add(time.Hour),
	}
	return &state, objectKey, hourKey
}

func assertLegacyNamingStateMigrated(t *testing.T, service *Service, state *uploadState, legacyObjectKey, hourKey string) {
	t.Helper()
	if state.Policy != service.policy || state.Policy.Naming != "codex56sol-jsonl-size-v1" {
		t.Fatalf("migrated policy = %+v, want %+v", state.Policy, service.policy)
	}
	if !state.dirty {
		t.Fatal("legacy naming policy migration did not mark state dirty")
	}
	object, exists := state.Objects[legacyObjectKey]
	if !exists || object.ObjectKey != legacyObjectKey {
		t.Fatalf("legacy object key was changed or removed: exists=%t object=%+v", exists, object)
	}
	hour, exists := state.Hours[hourKey]
	if !exists || hour.ObjectKey != legacyObjectKey {
		t.Fatalf("legacy hour reference was changed or removed: exists=%t hour=%+v", exists, hour)
	}
}
