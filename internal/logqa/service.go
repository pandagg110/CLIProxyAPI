package logqa

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// Service runs scheduled or one-shot QA over unuploaded logs.
type Service struct {
	cfg Config
}

// NewService constructs a QA service from config.
func NewService(cfg Config) *Service {
	return &Service{cfg: cfg}
}

// RunOnce executes a single QA pass.
func (s *Service) RunOnce(ctx context.Context) error {
	lock, errLock := acquireQALock(s.cfg.WorkDir)
	if errLock != nil {
		return errLock
	}
	defer func() {
		if err := lock.Release(); err != nil {
			log.WithError(err).Warn("release qa lock failed")
		}
	}()
	_, err := s.runLocked(ctx)
	return err
}

// Run schedules QA on interval until context cancel.
func (s *Service) Run(ctx context.Context) error {
	if s.cfg.Schedule.InitialDelay > 0 {
		log.WithField("initial_delay", s.cfg.Schedule.InitialDelay.String()).Info("log-qa waiting initial delay")
		timer := time.NewTimer(s.cfg.Schedule.InitialDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	if s.cfg.Schedule.RunOnStart {
		if err := s.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.WithError(err).Error("log-qa initial run failed")
		}
	}
	ticker := time.NewTicker(s.cfg.Schedule.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := s.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.WithError(err).Error("log-qa scheduled run failed")
			}
		}
	}
}

func (s *Service) runLocked(ctx context.Context) (RunSummary, error) {
	started := time.Now().In(s.cfg.Location)
	summary := RunSummary{
		RunID:          newRunID(started),
		StartedAt:      started,
		FailReasonHist: map[string]int{},
		AggregationKey: s.cfg.Aggregation.Key,
		LogsRoot:       s.cfg.LogsRoot,
		WorkDir:        s.cfg.WorkDir,
	}

	state, errState := loadState(s.cfg.WorkDir)
	if errState != nil {
		return summary, errState
	}

	type fileJob struct {
		path string
		info os.FileInfo
	}
	var jobs []fileJob
	now := time.Now()
	currentHour := now.In(s.cfg.Location).Truncate(time.Hour)
	seenFingerprints := make(map[string]struct{})

	errWalk := filepath.WalkDir(s.cfg.LogsRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".log") {
			return nil
		}
		info, errInfo := d.Info()
		if errInfo != nil {
			if os.IsNotExist(errInfo) {
				summary.FilesDisappeared++
				return nil
			}
			return errInfo
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return nil
		}
		summary.FilesSeen++

		rel, errRel := filepath.Rel(s.cfg.LogsRoot, path)
		if errRel != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if strings.Count(rel, "/") != 1 {
			// expect key_name/file.log
			return nil
		}

		if s.cfg.Scan.SkipCurrentHour {
			hour := info.ModTime().In(s.cfg.Location).Truncate(time.Hour)
			if !hour.Before(currentHour) {
				summary.FilesSkippedCurrentHour++
				return nil
			}
		}
		if s.cfg.Scan.MinFileAge > 0 && now.Sub(info.ModTime()) < s.cfg.Scan.MinFileAge {
			summary.FilesSkippedHot++
			return nil
		}
		if s.cfg.Scan.MaxFileSize > 0 && info.Size() > s.cfg.Scan.MaxFileSize {
			summary.FilesSkippedTooLarge++
			return nil
		}

		fp := fingerprint(rel, info.Size(), info.ModTime())
		seenFingerprints[fp] = struct{}{}
		if _, ok := state.Files[fp]; ok {
			summary.FilesSkippedUnchanged++
			return nil
		}
		jobs = append(jobs, fileJob{path: path, info: info})
		return nil
	})
	if errWalk != nil {
		return summary, fmt.Errorf("walk logs-root: %w", errWalk)
	}

	// Drop state entries for files no longer present (by path match).
	livePaths := make(map[string]struct{})
	_ = filepath.WalkDir(s.cfg.LogsRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(d.Name()), ".log") {
			return nil
		}
		rel, errRel := filepath.Rel(s.cfg.LogsRoot, path)
		if errRel == nil {
			livePaths[filepath.ToSlash(rel)] = struct{}{}
		}
		return nil
	})
	for fp, st := range state.Files {
		if _, ok := livePaths[st.Path]; !ok {
			delete(state.Files, fp)
		}
	}

	// Apply per-run limits. 0 means unlimited (full scan of all pending files).
	partial := false
	var bytesBudget int64
	limitedJobs := make([]fileJob, 0, len(jobs))
	for _, job := range jobs {
		if s.cfg.Scan.MaxFilesPerRun > 0 && len(limitedJobs) >= s.cfg.Scan.MaxFilesPerRun {
			partial = true
			break
		}
		if s.cfg.Scan.MaxBytesPerRun > 0 && bytesBudget+job.info.Size() > s.cfg.Scan.MaxBytesPerRun {
			partial = true
			break
		}
		limitedJobs = append(limitedJobs, job)
		bytesBudget += job.info.Size()
	}
	summary.Partial = partial

	// Parse new/changed files with limited concurrency
	type parseResult struct {
		rec         RequestRecord
		fp          string
		err         error
		disappeared bool
	}
	results := make([]parseResult, len(limitedJobs))
	sem := make(chan struct{}, s.cfg.Scan.MaxFileConcurrency)
	var wg sync.WaitGroup
	for i, job := range limitedJobs {
		if ctx.Err() != nil {
			return summary, ctx.Err()
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, j fileJob) {
			defer wg.Done()
			defer func() { <-sem }()
			rec, errParse := parseLogFile(s.cfg.LogsRoot, j.path, j.info, s.cfg.Location, s.cfg.Rules)
			if errParse != nil {
				if os.IsNotExist(errParse) {
					results[idx] = parseResult{disappeared: true}
					return
				}
				results[idx] = parseResult{err: errParse}
				return
			}
			results[idx] = parseResult{rec: rec, fp: rec.Fingerprint}
		}(i, job)
	}
	wg.Wait()

	scannedNow := make([]RequestRecord, 0, len(results))
	for _, r := range results {
		if r.disappeared {
			summary.FilesDisappeared++
			continue
		}
		if r.err != nil {
			summary.FilesParseError++
			log.WithError(r.err).Warn("log-qa parse failed")
			continue
		}
		summary.FilesScanned++
		summary.BytesScanned += r.rec.SizeBytes
		if r.rec.ParseError != "" {
			summary.FilesParseError++
		}
		state.Files[r.fp] = fileStateFromRequest(r.rec, now)
		scannedNow = append(scannedNow, r.rec)
	}

	// Build full request set from state (current live files) for aggregation
	allRequests := make([]RequestRecord, 0, len(state.Files))
	for fp, st := range state.Files {
		if _, ok := livePaths[st.Path]; !ok {
			continue
		}
		allRequests = append(allRequests, requestFromFileState(fp, st))
	}

	sessions := AggregateSessions(allRequests, s.cfg.Rules)
	total, pass, fail, hist, rate := summarizeSessions(sessions)
	summary.SessionsTotal = total
	summary.SessionsPass = pass
	summary.SessionsFail = fail
	summary.FailReasonHist = hist
	summary.PassRate = rate
	summary.FinishedAt = time.Now().In(s.cfg.Location)

	if errWrite := writeRunReport(s.cfg.WorkDir, summary, sessions, allRequests, s.cfg.Report.KeepRuns); errWrite != nil {
		return summary, errWrite
	}
	state.LastRunAt = summary.FinishedAt
	if errSave := saveState(s.cfg.WorkDir, state); errSave != nil {
		return summary, errSave
	}

	log.WithFields(log.Fields{
		"run_id":         summary.RunID,
		"files_seen":     summary.FilesSeen,
		"files_scanned":  summary.FilesScanned,
		"sessions_total": summary.SessionsTotal,
		"sessions_pass":  summary.SessionsPass,
		"sessions_fail":  summary.SessionsFail,
		"pass_rate":      summary.PassRate,
		"partial":        summary.Partial,
	}).Info("log-qa run completed")

	_ = scannedNow // kept for clarity; requests written from allRequests
	return summary, nil
}
