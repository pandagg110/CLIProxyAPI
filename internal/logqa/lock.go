package logqa

import (
	"fmt"
	"os"
	"path/filepath"
)

// tryLockQA creates an exclusive lock file. Caller must close/remove.
// Simple lockfile approach without flock for portability; atomic O_CREATE|O_EXCL.
type qaLock struct {
	path string
	file *os.File
}

func acquireQALock(workDir string) (*qaLock, error) {
	if err := os.MkdirAll(workDir, 0o750); err != nil {
		return nil, fmt.Errorf("create work dir: %w", err)
	}
	path := filepath.Join(workDir, "qa.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("another log-qa instance holds %s", path)
		}
		return nil, fmt.Errorf("create qa lock: %w", err)
	}
	if _, errWrite := fmt.Fprintf(f, "%d\n", os.Getpid()); errWrite != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("write qa lock: %w", errWrite)
	}
	return &qaLock{path: path, file: f}, nil
}

func (l *qaLock) Release() error {
	if l == nil {
		return nil
	}
	var errs []error
	if l.file != nil {
		if err := l.file.Close(); err != nil {
			errs = append(errs, err)
		}
		l.file = nil
	}
	if l.path != "" {
		if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
		l.path = ""
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}
