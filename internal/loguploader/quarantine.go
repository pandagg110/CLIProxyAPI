package loguploader

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	log "github.com/sirupsen/logrus"
)

const invalidSourceQuarantineDir = "quarantine-invalid"

func (s *Service) quarantineSource(source sourceLog, reason error) (string, error) {
	root := filepath.Join(filepath.Dir(filepath.Clean(s.cfg.LogsRoot)), invalidSourceQuarantineDir)
	destination := filepath.Join(root, filepath.FromSlash(source.Relative))
	if errMkdir := os.MkdirAll(filepath.Dir(destination), 0o750); errMkdir != nil {
		return "", fmt.Errorf("create invalid source quarantine directory: %w", errMkdir)
	}
	if _, errStat := os.Stat(destination); errStat == nil {
		suffix := fmt.Sprintf(".%x", sha256.Sum256([]byte(source.Fingerprint)))[:13]
		destination += suffix
	} else if !os.IsNotExist(errStat) {
		return "", fmt.Errorf("check invalid source quarantine destination: %w", errStat)
	}
	if errRename := os.Rename(source.Path, destination); errRename != nil {
		return "", fmt.Errorf("move invalid source log to quarantine: %w", errRename)
	}
	log.WithFields(log.Fields{
		"source":      source.Relative,
		"destination": destination,
		"reason":      reason.Error(),
	}).Warn("quarantined invalid source log")
	return destination, nil
}
