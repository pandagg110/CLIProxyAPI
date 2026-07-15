package loguploader

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func sourceFileMatches(path string, uploaded uploadedSource) (bool, error) {
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return false, errOpen
	}
	before, errBefore := file.Stat()
	if errBefore != nil {
		_ = file.Close()
		return false, fmt.Errorf("stat source before checksum: %w", errBefore)
	}
	hash := sha256.New()
	_, errCopy := io.Copy(hash, file)
	after, errAfter := file.Stat()
	errClose := file.Close()
	if errCombined := errors.Join(errCopy, errAfter, errClose); errCombined != nil {
		return false, fmt.Errorf("checksum source before deletion: %w", errCombined)
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return false, nil
	}
	checksum := fmt.Sprintf("%x", hash.Sum(nil))
	return after.Size() == uploaded.Size && after.ModTime().Equal(uploaded.ModTime) && checksum == uploaded.SHA256, nil
}

func (s *Service) pendingDeletePath(fingerprint string) string {
	name := fmt.Sprintf("%x.log", sha256.Sum256([]byte(fingerprint)))
	return filepath.Join(s.cfg.WorkDir, "delete-pending", name)
}
