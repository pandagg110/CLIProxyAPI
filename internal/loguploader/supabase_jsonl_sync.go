package loguploader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// SupabaseJSONLSyncSummary reports sanitized JSONL size backfill totals.
type SupabaseJSONLSyncSummary struct {
	Records             int   `json:"records"`
	Pending             int   `json:"pending"`
	LiveManaged         int   `json:"live_managed"`
	AlreadyCheckpointed int   `json:"already_checkpointed"`
	SkippedNoJSONL      int   `json:"skipped_no_jsonl"`
	Attempted           int   `json:"attempted"`
	Inserted            int   `json:"inserted"`
	Duplicate           int   `json:"duplicate"`
	Checkpointed        int   `json:"checkpointed"`
	SourceCount         int64 `json:"source_count"`
	JSONLBytes          int64 `json:"jsonl_bytes"`
	FilenameMismatches  int   `json:"filename_mismatches"`
}

// SyncSupabaseJSONL sends exact per-key JSONL usage for already uploaded
// archives. Live-managed hours are skipped because they already used exact
// JSONL. Objects previously synced as batch-only history are included.
func (s *Service) SyncSupabaseJSONL(ctx context.Context, dryRun bool) (summary SupabaseJSONLSyncSummary, syncErr error) {
	if dryRun {
		return s.syncSupabaseJSONL(ctx, true)
	}
	lock, errLock := s.acquireWorkDirLock()
	if errLock != nil {
		return summary, errLock
	}
	defer func() {
		syncErr = errors.Join(syncErr, lock.Close())
	}()
	return s.syncSupabaseJSONL(ctx, dryRun)
}

func (s *Service) syncSupabaseJSONL(ctx context.Context, dryRun bool) (SupabaseJSONLSyncSummary, error) {
	var summary SupabaseJSONLSyncSummary
	if !s.cfg.Supabase.Enabled {
		return summary, fmt.Errorf("Supabase JSONL synchronization is disabled")
	}
	destinationID, errDestination := supabaseDestinationID(s.cfg.Supabase.IngestURL)
	if errDestination != nil {
		return summary, fmt.Errorf("Supabase history destination is invalid")
	}
	ledger, errLedger := readSupabaseHistoryLedger(s.cfg.WorkDir, s.location)
	if errLedger != nil {
		return summary, errLedger
	}
	state, errState := s.loadState()
	if errState != nil {
		return summary, fmt.Errorf("trusted upload state is invalid")
	}
	uploads, preflightSummary, errPreflight := s.preflightSupabaseJSONLSync(state, ledger, destinationID)
	if errPreflight != nil {
		return summary, errPreflight
	}
	summary = preflightSummary
	if dryRun {
		return summary, nil
	}
	pruned := false
	for checkpointKey, checkpoint := range state.SupabaseJSONLSync {
		if checkpoint.DestinationID == destinationID {
			continue
		}
		delete(state.SupabaseJSONLSync, checkpointKey)
		pruned = true
	}
	if pruned {
		if _, errSave := s.saveStateWithResult(state); errSave != nil {
			return summary, errSupabaseDeliveryState
		}
	}
	if len(uploads) == 0 {
		return summary, nil
	}

	for _, upload := range uploads {
		select {
		case <-ctx.Done():
			return summary, errSupabaseDeliveryRetryable
		default:
		}
		if _, checkpointed := state.SupabaseJSONLSync[upload.checkpointKey]; checkpointed {
			continue
		}
		if existing, exists := state.SupabaseOutbox.Entries[upload.entry.EventID]; exists {
			if !sameSupabaseHistoryOutboxEntry(existing, upload.entry) {
				return summary, fmt.Errorf("pending Supabase JSONL event does not match local audit state")
			}
		} else {
			state.SupabaseOutbox.Entries[upload.entry.EventID] = upload.entry
			published, errSave := s.saveStateWithResult(state)
			if errSave != nil {
				if !published {
					delete(state.SupabaseOutbox.Entries, upload.entry.EventID)
				}
				return summary, errSupabaseDeliveryState
			}
		}

		delivery, errDelivery := s.drainSupabaseOutboxWithPreferredEvent(ctx, &state, upload.entry.EventID)
		summary.Attempted += delivery.Attempted
		summary.Inserted += delivery.Inserted
		summary.Duplicate += delivery.Duplicate
		if errDelivery != nil {
			return summary, errDelivery
		}
		if delivery.Inserted+delivery.Duplicate != 1 {
			return summary, fmt.Errorf("Supabase JSONL event was not acknowledged")
		}
		if _, stillPending := state.SupabaseOutbox.Entries[upload.entry.EventID]; stillPending {
			return summary, fmt.Errorf("acknowledged Supabase JSONL event remains pending")
		}

		state.SupabaseJSONLSync[upload.checkpointKey] = upload.checkpoint
		published, errCheckpoint := s.saveStateWithResult(state)
		if errCheckpoint != nil {
			delete(state.SupabaseJSONLSync, upload.checkpointKey)
			if published {
				if errRollback := s.saveState(state); errRollback != nil {
					return summary, errors.Join(errSupabaseDeliveryState, errRollback)
				}
			}
			return summary, errSupabaseDeliveryState
		}
		summary.Checkpointed++
	}
	return summary, nil
}

func (s *Service) preflightSupabaseJSONLSync(state uploadState, ledger supabaseHistoryLedger, destinationID string) ([]supabaseHistoryUpload, SupabaseJSONLSyncSummary, error) {
	summary := SupabaseJSONLSyncSummary{
		Records:     len(ledger.Records),
		SourceCount: ledger.Summary.SourceCount,
		JSONLBytes:  ledger.Summary.JSONLBytes,
	}
	hourByObject := make(map[string]string, len(state.Hours))
	for hourKey, hour := range state.Hours {
		hourByObject[hour.ObjectKey] = hourKey
	}

	activeEntries, existingPayloadBytes, errCapacity := supabaseOutboxActiveCapacity(state.SupabaseOutbox.Entries)
	if errCapacity != nil {
		return nil, summary, fmt.Errorf("Supabase JSONL event cannot be queued: %w", errCapacity)
	}
	uploads := make([]supabaseHistoryUpload, 0, len(ledger.Records))
	checkpointKeys := make(map[string]struct{}, len(ledger.Records))
	for _, record := range ledger.Records {
		object, objectExists := state.Objects[record.ObjectKey]
		hourKey, hourExists := hourByObject[record.ObjectKey]
		if !objectExists || !hourExists {
			return nil, summary, fmt.Errorf("history ledger does not match trusted upload state")
		}
		hour := state.Hours[hourKey]
		stateProvider := record.Provider
		if stateProvider == "" {
			stateProvider = providerCodex
		}
		if hourStateKey(record.Hour.In(s.location), stateProvider) != hourKey ||
			hour.ObjectKey != record.ObjectKey || hour.ArchiveSHA256 != object.ArchiveSHA256 ||
			record.CompressedBytes != object.CompressedSize {
			return nil, summary, fmt.Errorf("history ledger does not match trusted upload state")
		}
		if label := archiveFilenameJSONLLabel(record.ObjectKey); label != "" && humanSize(record.JSONLBytes) != label {
			summary.FilenameMismatches++
		}
		if hour.SupabaseEventID != "" {
			if record.SupabaseEventID != "" && record.SupabaseEventID != hour.SupabaseEventID {
				return nil, summary, fmt.Errorf("history ledger does not match trusted upload state")
			}
			summary.LiveManaged++
			continue
		}
		payload, errPayload := s.buildSupabaseHistoryPayload(record, hour, object)
		if errPayload != nil {
			return nil, summary, fmt.Errorf("history ledger cannot produce a valid Supabase event")
		}
		if payload.UsagePrecision == supabaseUsagePrecisionBatchOnly {
			summary.SkippedNoJSONL++
			continue
		}
		rawPayload, errMarshal := json.Marshal(payload)
		if errMarshal != nil {
			return nil, summary, fmt.Errorf("marshal Supabase JSONL event")
		}
		payloadSHA256 := sha256.Sum256(rawPayload)
		entry := supabaseOutboxEntry{
			EventID:       payload.EventID,
			HourKey:       hourKey,
			ObjectKey:     record.ObjectKey,
			Status:        supabaseOutboxStatusPending,
			Payload:       bytes.Clone(rawPayload),
			PayloadSHA256: fmt.Sprintf("%x", payloadSHA256),
			EnqueuedAt:    s.now().In(s.location),
		}
		checkpointKey := supabaseJSONLSyncCheckpointKey(destinationID, record.ObjectKey)
		checkpoint := supabaseHistoryCheckpoint{
			DestinationID: destinationID,
			ObjectKey:     record.ObjectKey,
			ArchiveSHA256: object.ArchiveSHA256,
			EventID:       payload.EventID,
			CommittedAt:   s.now().In(s.location),
		}
		if existing, checkpointed := state.SupabaseJSONLSync[checkpointKey]; checkpointed {
			if existing != checkpoint {
				if existing.DestinationID != checkpoint.DestinationID || existing.ObjectKey != checkpoint.ObjectKey ||
					existing.ArchiveSHA256 != checkpoint.ArchiveSHA256 || existing.EventID != checkpoint.EventID || existing.CommittedAt.IsZero() {
					return nil, summary, fmt.Errorf("Supabase JSONL checkpoint conflicts with trusted upload state")
				}
			}
			summary.AlreadyCheckpointed++
			continue
		}
		if _, duplicate := checkpointKeys[checkpointKey]; duplicate {
			return nil, summary, fmt.Errorf("history ledger contains a duplicate JSONL checkpoint identity")
		}
		checkpointKeys[checkpointKey] = struct{}{}
		if existing, exists := state.SupabaseOutbox.Entries[entry.EventID]; exists {
			if !sameSupabaseHistoryOutboxEntry(existing, entry) {
				return nil, summary, fmt.Errorf("pending Supabase JSONL event conflicts with trusted upload state")
			}
		} else if errCapacity := validateSupabaseOutboxCapacity(activeEntries, existingPayloadBytes, int64(len(entry.Payload))); errCapacity != nil {
			return nil, summary, fmt.Errorf("Supabase JSONL event cannot be queued: %w", errCapacity)
		}
		uploads = append(uploads, supabaseHistoryUpload{checkpointKey: checkpointKey, checkpoint: checkpoint, entry: entry})
		summary.Pending++
	}
	sort.Slice(uploads, func(i, j int) bool {
		if uploads[i].entry.HourKey != uploads[j].entry.HourKey {
			return uploads[i].entry.HourKey < uploads[j].entry.HourKey
		}
		return uploads[i].entry.EventID < uploads[j].entry.EventID
	})
	return uploads, summary, nil
}

func supabaseJSONLSyncCheckpointKey(destinationID, objectKey string) string {
	digest := sha256.Sum256([]byte("cliproxy-supabase-jsonl-sync-v1" + destinationID + objectKey))
	return fmt.Sprintf("%x", digest)
}
