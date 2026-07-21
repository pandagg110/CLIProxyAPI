package loguploader

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	log "github.com/sirupsen/logrus"
)

func (s *Service) resumePreparedHours(ctx context.Context, state uploadState) error {
	hourKeys := make([]string, 0, len(state.PreparedHours))
	for hourKey := range state.PreparedHours {
		hourKeys = append(hourKeys, hourKey)
	}
	sort.Strings(hourKeys)
	if len(hourKeys) > 0 {
		log.WithField("count", len(hourKeys)).Info("resuming prepared hours")
	}
	var resumeErrors []error
	for _, hourKey := range hourKeys {
		prepared, exists := state.PreparedHours[hourKey]
		if !exists {
			continue
		}
		log.WithFields(log.Fields{
			"hour":           prepared.Hour.Format(time.RFC3339),
			"object_key":     prepared.ObjectKey,
			"archive_size":   prepared.CompressedBytes,
		}).Info("resuming prepared hour")
		if errComplete := s.completePreparedHour(ctx, hourKey, prepared, state); errComplete != nil {
			resumeErrors = append(resumeErrors, errComplete)
		}
	}
	return errors.Join(resumeErrors...)
}

func (s *Service) completePreparedHour(ctx context.Context, hourKey string, prepared preparedHour, state uploadState) error {
	record := s.auditRecordForPrepared(prepared)
	if prepared.TargetID != s.target.ID {
		return s.recordBatchFailure(record, fmt.Errorf("prepared hour target does not match configured upload target"))
	}
	if gotManifest := manifestSHA256(prepared.Sources); gotManifest != prepared.ManifestSHA256 {
		return s.recordBatchFailure(record, fmt.Errorf("prepared hour manifest checksum mismatch: got %s, want %s", gotManifest, prepared.ManifestSHA256))
	}
	archiveRoot := filepath.Join(s.cfg.WorkDir, "archives")
	archivePath, errPath := safeExistingPath(archiveRoot, prepared.ArchivePath)
	if errPath != nil {
		return s.recordBatchFailure(record, errPath)
	}
	// Fast verification: check file size only. SHA256 was already computed
	// and stored in state.json during processBatch. Re-hashing a 10+ GB
	// archive on every resume adds tens of minutes of delay.
	info, errStat := os.Stat(archivePath)
	if errStat != nil {
		return s.recordBatchFailure(record, fmt.Errorf("stat prepared archive: %w", errStat))
	}
	if info.Size() != prepared.CompressedBytes {
		return s.recordBatchFailure(record, fmt.Errorf("prepared archive size mismatch: got %d, want %d", info.Size(), prepared.CompressedBytes))
	}
	log.WithFields(log.Fields{
		"hour":         prepared.Hour.Format(time.RFC3339),
		"archive_size": info.Size(),
	}).Info("prepared archive verified (size check)")
	if s.uploader == nil {
		return s.recordBatchFailure(record, fmt.Errorf("upload is enabled but no object uploader is configured"))
	}

	uploadStart := s.now()
	errUpload := s.uploader.UploadFile(ctx, s.cfg.Upload.Bucket, prepared.ObjectKey, archivePath)
	if errors.Is(errUpload, ErrObjectConflict) {
		matcher, supportsMatch := s.uploader.(ObjectMatcher)
		if !supportsMatch {
			return s.recordBatchFailure(record, fmt.Errorf("upload %s: verify existing object: uploader does not support checksum matching: %w", prepared.ObjectKey, errUpload))
		}
		matches, errMatch := matcher.MatchObject(ctx, s.cfg.Upload.Bucket, prepared.ObjectKey, archivePath)
		if errMatch != nil {
			return s.recordBatchFailure(record, fmt.Errorf("upload %s: verify existing object after conflict: %w", prepared.ObjectKey, errMatch))
		}
		if !matches {
			return s.recordBatchFailure(record, fmt.Errorf("upload %s: existing object checksum or size differs; prepared batch retained: %w", prepared.ObjectKey, errUpload))
		}
		log.WithField("object_key", prepared.ObjectKey).Warn("matching remote archive already exists; recovering prepared upload state")
		errUpload = nil
	}
	if errUpload != nil {
		return s.recordBatchFailure(record, fmt.Errorf("upload %s: %w", prepared.ObjectKey, errUpload))
	}
	log.WithFields(log.Fields{
		"hour":             prepared.Hour.Format(time.RFC3339),
		"object_key":       prepared.ObjectKey,
		"archive_size":     info.Size(),
		"upload_duration":  s.now().Sub(uploadStart).String(),
	}).Info("prepared hour uploaded")

	needsCleanup := s.cfg.Retention.DeleteSourceAfterUpload || !s.cfg.Retention.KeepLocalArchives
	preCleanupRecord := record
	if needsCleanup {
		preCleanupRecord.Status = "uploaded_cleanup_pending"
	} else {
		preCleanupRecord.Status = "uploaded"
	}
	if errAudit := s.appendAudit(preCleanupRecord); errAudit != nil {
		return fmt.Errorf("record successful upload before committing prepared state: %w", errAudit)
	}

	uploadedAt := s.now().In(s.location)
	for _, source := range prepared.Sources {
		if _, exists := state.Uploaded[source.Fingerprint]; exists {
			return fmt.Errorf("prepared source %s is already committed", source.RelativePath)
		}
		state.Uploaded[source.Fingerprint] = uploadedSource{
			ObjectKey:    prepared.ObjectKey,
			HourKey:      hourKey,
			TargetID:     s.target.ID,
			UploadedAt:   uploadedAt,
			RelativePath: source.RelativePath,
			Size:         source.Size,
			ModTime:      source.ModTime,
			SHA256:       source.SHA256,
		}
	}
	state.Objects[prepared.ObjectKey] = uploadedObject{
		ObjectKey:      prepared.ObjectKey,
		CompressedSize: prepared.CompressedBytes,
		ArchiveSHA256:  prepared.ArchiveSHA256,
		Verification:   "put-success-or-remote-head-match",
		UploadedAt:     uploadedAt,
		VerifiedAt:     uploadedAt,
		ArchivePath:    archivePath,
	}
	state.Hours[hourKey] = uploadedHour{
		Status:         "sealed",
		ObjectKey:      prepared.ObjectKey,
		ArchiveSHA256:  prepared.ArchiveSHA256,
		ManifestSHA256: prepared.ManifestSHA256,
		UploadedAt:     uploadedAt,
	}
	delete(state.PreparedHours, hourKey)
	if errSave := s.saveState(state); errSave != nil {
		for _, source := range prepared.Sources {
			delete(state.Uploaded, source.Fingerprint)
		}
		delete(state.Objects, prepared.ObjectKey)
		delete(state.Hours, hourKey)
		state.PreparedHours[hourKey] = prepared
		return fmt.Errorf("commit uploaded prepared hour: %w", errSave)
	}

	if !needsCleanup {
		logPreparedUpload(record)
		return nil
	}
	fingerprints := make([]string, 0, len(prepared.Sources))
	for _, source := range prepared.Sources {
		fingerprints = append(fingerprints, source.Fingerprint)
	}
	if s.cfg.Retention.DeleteSourceAfterUpload {
		changed, deleteErrors := s.deleteUploadedSources(state, fingerprints, &record.DeletedSources)
		if changed {
			if errSave := s.saveState(state); errSave != nil {
				deleteErrors = append(deleteErrors, errSave)
			}
		}
		if len(deleteErrors) > 0 {
			record.Status = "uploaded_delete_pending"
			record.Error = errors.Join(deleteErrors...).Error()
			for _, errDelete := range deleteErrors {
				log.WithError(errDelete).Error("failed to finish uploaded source cleanup")
			}
		}
	}
	if !s.cfg.Retention.KeepLocalArchives {
		changed, archiveErrors := s.deleteLocalArchives(state, []string{prepared.ObjectKey})
		if changed {
			if errSave := s.saveState(state); errSave != nil {
				archiveErrors = append(archiveErrors, errSave)
			}
		}
		if len(archiveErrors) > 0 {
			if record.Status == "uploaded_delete_pending" {
				record.Status = "uploaded_cleanup_pending"
			} else {
				record.Status = "uploaded_archive_delete_pending"
			}
			archiveError := errors.Join(archiveErrors...).Error()
			if record.Error == "" {
				record.Error = archiveError
			} else {
				record.Error += "; " + archiveError
			}
			for _, errDelete := range archiveErrors {
				log.WithError(errDelete).WithField("archive", archivePath).Warn("failed to remove uploaded local archive")
			}
		}
	}
	if record.Status == "" {
		record.Status = "uploaded"
	}
	if errAudit := s.appendAudit(record); errAudit != nil {
		return errAudit
	}
	logPreparedUpload(record)
	return nil
}

func (s *Service) auditRecordForPrepared(prepared preparedHour) auditRecord {
	record := auditRecord{
		Timestamp:       s.now().In(s.location),
		Provider:        prepared.Provider,
		Hour:            prepared.Hour,
		SourceCount:     len(prepared.Sources),
		KeyNames:        make(map[string]auditKeyNameSummary),
		JSONLBytes:      prepared.JSONLBytes,
		CompressedBytes: prepared.CompressedBytes,
		ObjectKey:       prepared.ObjectKey,
		ArchivePath:     prepared.ArchivePath,
	}
	for _, source := range prepared.Sources {
		record.SourceBytes += source.Size
		keySummary := record.KeyNames[source.KeyName]
		keySummary.SourceCount++
		keySummary.SourceBytes += source.Size
		if keySummary.Models == nil {
			keySummary.Models = make(map[string]auditModelSummary)
		}
		modelSummary := keySummary.Models[source.Model]
		modelSummary.SourceCount++
		modelSummary.SourceBytes += source.Size
		keySummary.Models[source.Model] = modelSummary
		record.KeyNames[source.KeyName] = keySummary
	}
	return record
}

func logPreparedUpload(record auditRecord) {
	log.WithFields(log.Fields{
		"hour":       record.Hour.Format(time.RFC3339),
		"key_names":  len(record.KeyNames),
		"records":    record.SourceCount,
		"object_key": record.ObjectKey,
	}).Info("log archive uploaded")
}
