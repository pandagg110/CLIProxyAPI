package loguploader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateUploadStateRejectsInconsistentReferences(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 5, 10, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	service := mustTestService(t, testConfig(root, workDir), nil, now)

	if errValidate := service.validateUploadState(validSecurityUploadState(service)); errValidate != nil {
		t.Fatalf("valid state rejected: %v", errValidate)
	}

	tests := []struct {
		name    string
		wantErr string
		mutate  func(state *uploadState)
	}{
		{
			name:    "non-deterministic pending delete path",
			wantErr: "invalid pending delete path",
			mutate: func(state *uploadState) {
				fingerprint := securityUploadedFingerprint(service)
				source := state.Uploaded[fingerprint]
				source.PendingDeleteAt = filepath.Join(workDir, "delete-pending", "different.log")
				state.Uploaded[fingerprint] = source
			},
		},
		{
			name:    "object map key mismatch",
			wantErr: "does not match object key",
			mutate: func(state *uploadState) {
				objectKey := securityUploadedObjectKey()
				object := state.Objects[objectKey]
				object.ObjectKey = "cliproxy-logs/wrong-object.jsonl.zst"
				state.Objects[objectKey] = object
			},
		},
		{
			name:    "object checksum is invalid",
			wantErr: "invalid archive checksum",
			mutate: func(state *uploadState) {
				objectKey := securityUploadedObjectKey()
				object := state.Objects[objectKey]
				object.ArchiveSHA256 = "not-a-sha256"
				state.Objects[objectKey] = object
			},
		},
		{
			name:    "hour references missing object",
			wantErr: "references missing object",
			mutate: func(state *uploadState) {
				delete(state.Objects, securityUploadedObjectKey())
			},
		},
		{
			name:    "hour and object checksums differ",
			wantErr: "archive checksum does not match",
			mutate: func(state *uploadState) {
				hour := state.Hours["2026-07-15-01"]
				hour.ArchiveSHA256 = strings.Repeat("9", 64)
				state.Hours["2026-07-15-01"] = hour
			},
		},
		{
			name:    "orphan object",
			wantErr: "is not referenced by a sealed hour",
			mutate: func(state *uploadState) {
				objectKey := "cliproxy-logs/2026/07/15/orphan.jsonl.zst"
				state.Objects[objectKey] = uploadedObject{ObjectKey: objectKey, ArchiveSHA256: strings.Repeat("8", 64)}
			},
		},
		{
			name:    "uploaded fingerprint key mismatch",
			wantErr: "does not match its source identity",
			mutate: func(state *uploadState) {
				fingerprint := securityUploadedFingerprint(service)
				source := state.Uploaded[fingerprint]
				delete(state.Uploaded, fingerprint)
				state.Uploaded["wrong-fingerprint"] = source
			},
		},
		{
			name:    "uploaded object differs from sealed hour",
			wantErr: "does not reference a sealed hour and object",
			mutate: func(state *uploadState) {
				fingerprint := securityUploadedFingerprint(service)
				source := state.Uploaded[fingerprint]
				source.ObjectKey = "cliproxy-logs/missing.jsonl.zst"
				state.Uploaded[fingerprint] = source
			},
		},
		{
			name:    "prepared manifest mismatch",
			wantErr: "manifest checksum mismatch",
			mutate: func(state *uploadState) {
				prepared := state.PreparedHours["2026-07-15-02"]
				prepared.ManifestSHA256 = strings.Repeat("0", 64)
				state.PreparedHours["2026-07-15-02"] = prepared
			},
		},
		{
			name:    "prepared object is already committed",
			wantErr: "reuses committed object",
			mutate: func(state *uploadState) {
				prepared := state.PreparedHours["2026-07-15-02"]
				prepared.ObjectKey = securityUploadedObjectKey()
				state.PreparedHours["2026-07-15-02"] = prepared
			},
		},
		{
			name:    "prepared and uploaded share fingerprint",
			wantErr: "is shared by uploaded source and prepared hour",
			mutate: func(state *uploadState) {
				fingerprint := securityUploadedFingerprint(service)
				uploaded := state.Uploaded[fingerprint]
				prepared := state.PreparedHours["2026-07-15-02"]
				prepared.Sources = []preparedSource{{
					Fingerprint:  fingerprint,
					RelativePath: uploaded.RelativePath,
					KeyName:      "panda",
					Model:        "model-a",
					Size:         uploaded.Size,
					ModTime:      uploaded.ModTime,
					SHA256:       uploaded.SHA256,
				}}
				prepared.ManifestSHA256 = manifestSHA256(prepared.Sources)
				state.PreparedHours["2026-07-15-02"] = prepared
			},
		},
		{
			name:    "prepared hours share fingerprint",
			wantErr: "is shared by prepared hour",
			mutate: func(state *uploadState) {
				original := state.PreparedHours["2026-07-15-02"]
				hour := time.Date(2026, time.July, 15, 3, 0, 0, 0, service.location)
				duplicate := original
				duplicate.Hour = hour
				duplicate.ObjectKey = "cliproxy-logs/2026/07/15/2026-07-15-03-codex56sol-1K.jsonl.zst"
				duplicate.ArchivePath = filepath.Join(service.cfg.WorkDir, "archives", "2026", "07", "15", filepath.Base(duplicate.ObjectKey))
				duplicate.Sources = append([]preparedSource(nil), original.Sources...)
				duplicate.ManifestSHA256 = manifestSHA256(duplicate.Sources)
				state.PreparedHours["2026-07-15-03"] = duplicate
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := validSecurityUploadState(service)
			test.mutate(state)
			errValidate := service.validateUploadState(state)
			if errValidate == nil || !strings.Contains(errValidate.Error(), test.wantErr) {
				t.Fatalf("validateUploadState error = %v, want %q", errValidate, test.wantErr)
			}
		})
	}
}

func TestValidateUploadStateAcceptsDeterministicPendingDeletePath(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 5, 10, 0, 0, location)
	service := mustTestService(t, testConfig(filepath.Join(t.TempDir(), "keys"), filepath.Join(t.TempDir(), "uploader")), nil, now)
	state := validSecurityUploadState(service)
	fingerprint := securityUploadedFingerprint(service)
	source := state.Uploaded[fingerprint]
	source.PendingDeleteAt = service.pendingDeletePath(fingerprint)
	state.Uploaded[fingerprint] = source

	if errValidate := service.validateUploadState(state); errValidate != nil {
		t.Fatalf("deterministic pending delete path rejected: %v", errValidate)
	}
}

func TestDeleteUploadedSourcesRejectsNonDeterministicPendingPath(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 5, 10, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	service := mustTestService(t, testConfig(root, workDir), nil, now)
	outsidePath := filepath.Join(t.TempDir(), "outside.log")
	if errWrite := os.WriteFile(outsidePath, []byte("must remain"), 0o600); errWrite != nil {
		t.Fatalf("write outside file: %v", errWrite)
	}
	fingerprint := "panda/source.log|11|1"
	state := service.newUploadState()
	state.Uploaded[fingerprint] = uploadedSource{
		TargetID:        service.target.ID,
		RelativePath:    "panda/source.log",
		SHA256:          strings.Repeat("a", 64),
		PendingDeleteAt: outsidePath,
	}
	deleted := 0

	changed, deleteErrors := service.deleteUploadedSources(state, []string{fingerprint}, &deleted)
	if changed || deleted != 0 || len(deleteErrors) != 1 || !strings.Contains(deleteErrors[0].Error(), "invalid pending delete path") {
		t.Fatalf("delete result changed=%t deleted=%d errors=%v", changed, deleted, deleteErrors)
	}
	if _, errStat := os.Stat(outsidePath); errStat != nil {
		t.Fatalf("non-deterministic pending path was touched: %v", errStat)
	}
	if _, exists := state.Uploaded[fingerprint]; !exists {
		t.Fatal("uploaded state was removed after rejecting pending path")
	}
}

func TestDeleteUploadedSourcesResumesDeterministicPendingPath(t *testing.T) {
	t.Parallel()

	location := mustLocation(t, "Asia/Shanghai")
	now := time.Date(2026, time.July, 15, 5, 10, 0, 0, location)
	root := filepath.Join(t.TempDir(), "keys")
	workDir := filepath.Join(t.TempDir(), "uploader")
	service := mustTestService(t, testConfig(root, workDir), nil, now)
	stagingPath := filepath.Join(workDir, "staging.log")
	if errMkdir := os.MkdirAll(workDir, 0o750); errMkdir != nil {
		t.Fatalf("create work directory: %v", errMkdir)
	}
	if errWrite := os.WriteFile(stagingPath, []byte("uploaded source"), 0o600); errWrite != nil {
		t.Fatalf("write staged source: %v", errWrite)
	}
	info, errStat := os.Stat(stagingPath)
	if errStat != nil {
		t.Fatalf("stat staged source: %v", errStat)
	}
	relativePath := "panda/source.log"
	fingerprint := sourceFingerprint(relativePath, info.Size(), info.ModTime())
	pendingPath := service.pendingDeletePath(fingerprint)
	if errMkdir := os.MkdirAll(filepath.Dir(pendingPath), 0o700); errMkdir != nil {
		t.Fatalf("create pending directory: %v", errMkdir)
	}
	if errRename := os.Rename(stagingPath, pendingPath); errRename != nil {
		t.Fatalf("stage deterministic pending source: %v", errRename)
	}
	checksum, _, errChecksum := fileSHA256(pendingPath)
	if errChecksum != nil {
		t.Fatalf("checksum pending source: %v", errChecksum)
	}
	state := service.newUploadState()
	state.Uploaded[fingerprint] = uploadedSource{
		TargetID:        service.target.ID,
		RelativePath:    relativePath,
		Size:            info.Size(),
		ModTime:         info.ModTime(),
		SHA256:          checksum,
		PendingDeleteAt: pendingPath,
	}
	deleted := 0

	changed, deleteErrors := service.deleteUploadedSources(state, []string{fingerprint}, &deleted)
	if !changed || deleted != 1 || len(deleteErrors) != 0 {
		t.Fatalf("delete result changed=%t deleted=%d errors=%v", changed, deleted, deleteErrors)
	}
	if _, errStat := os.Stat(pendingPath); !os.IsNotExist(errStat) {
		t.Fatalf("deterministic pending source still exists: %v", errStat)
	}
	if _, exists := state.Uploaded[fingerprint]; exists {
		t.Fatal("uploaded state remains after deterministic pending deletion")
	}
}

func validSecurityUploadState(service *Service) *uploadState {
	state := service.newUploadState()
	uploadedHour := time.Date(2026, time.July, 15, 1, 0, 0, 0, service.location)
	uploadedModTime := uploadedHour.Add(10 * time.Minute)
	uploadedRelativePath := "panda/source.log"
	uploadedFingerprint := sourceFingerprint(uploadedRelativePath, 100, uploadedModTime)
	uploadedObjectKey := securityUploadedObjectKey()
	uploadedArchiveSHA := strings.Repeat("a", 64)
	state.Objects[uploadedObjectKey] = uploadedObject{
		ObjectKey:      uploadedObjectKey,
		CompressedSize: 80,
		ArchiveSHA256:  uploadedArchiveSHA,
		Verification:   "put-success-or-remote-head-match",
		UploadedAt:     uploadedHour.Add(time.Hour),
		VerifiedAt:     uploadedHour.Add(time.Hour),
	}
	state.Hours[hourStateKey(uploadedHour)] = uploadedHourState(uploadedObjectKey, uploadedArchiveSHA, uploadedHour.Add(time.Hour))
	state.Uploaded[uploadedFingerprint] = uploadedSource{
		ObjectKey:    uploadedObjectKey,
		HourKey:      hourStateKey(uploadedHour),
		TargetID:     service.target.ID,
		UploadedAt:   uploadedHour.Add(time.Hour),
		RelativePath: uploadedRelativePath,
		Size:         100,
		ModTime:      uploadedModTime,
		SHA256:       strings.Repeat("b", 64),
	}

	preparedTime := time.Date(2026, time.July, 15, 2, 0, 0, 0, service.location)
	preparedModTime := preparedTime.Add(20 * time.Minute)
	preparedRelativePath := "alice/prepared.log"
	preparedSourceFingerprint := sourceFingerprint(preparedRelativePath, 120, preparedModTime)
	preparedObjectKey := "cliproxy-logs/2026/07/15/2026-07-15-02-codex56sol-1K.jsonl.zst"
	prepared := preparedHour{
		TargetID:        service.target.ID,
		Hour:            preparedTime,
		ObjectKey:       preparedObjectKey,
		ArchivePath:     filepath.Join(service.cfg.WorkDir, "archives", "2026", "07", "15", filepath.Base(preparedObjectKey)),
		JSONLBytes:      1024,
		CompressedBytes: 512,
		ArchiveSHA256:   strings.Repeat("c", 64),
		PreparedAt:      preparedTime.Add(time.Hour),
		Sources: []preparedSource{{
			Fingerprint:  preparedSourceFingerprint,
			RelativePath: preparedRelativePath,
			KeyName:      "alice",
			Model:        "model-b",
			Size:         120,
			ModTime:      preparedModTime,
			SHA256:       strings.Repeat("d", 64),
		}},
	}
	prepared.ManifestSHA256 = manifestSHA256(prepared.Sources)
	state.PreparedHours[hourStateKey(preparedTime)] = prepared
	return &state
}

func uploadedHourState(objectKey, archiveSHA string, uploadedAt time.Time) uploadedHour {
	return uploadedHour{
		Status:         "sealed",
		ObjectKey:      objectKey,
		ArchiveSHA256:  archiveSHA,
		ManifestSHA256: strings.Repeat("e", 64),
		UploadedAt:     uploadedAt,
	}
}

func securityUploadedObjectKey() string {
	return "cliproxy-logs/2026/07/15/2026-07-15-01-codex56sol-1K.jsonl.zst"
}

func securityUploadedFingerprint(service *Service) string {
	hour := time.Date(2026, time.July, 15, 1, 0, 0, 0, service.location)
	return sourceFingerprint("panda/source.log", 100, hour.Add(10*time.Minute))
}
