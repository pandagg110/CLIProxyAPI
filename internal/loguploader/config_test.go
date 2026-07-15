package loguploader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExampleConfigLoadsWithHourlyProductionDefaults(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "log-uploader.example.yaml")
	cfg, errLoad := LoadConfig(path)
	if errLoad != nil {
		t.Fatalf("load example config: %v", errLoad)
	}
	if cfg.Schedule.Interval != time.Hour || cfg.Schedule.SettleDelay != 5*time.Minute {
		t.Errorf("schedule = interval %s, settle %s", cfg.Schedule.Interval, cfg.Schedule.SettleDelay)
	}
	if cfg.Upload.Enabled {
		t.Errorf("example unexpectedly enables upload")
	}
	if !cfg.Schedule.RunOnStart {
		t.Errorf("example should scan completed historical hours on startup")
	}
	if !cfg.Retention.DeleteSourceAfterUpload || cfg.Retention.KeepLocalArchives {
		t.Errorf("unexpected production retention settings: delete_source=%t keep_archives=%t", cfg.Retention.DeleteSourceAfterUpload, cfg.Retention.KeepLocalArchives)
	}
	if cfg.Upload.Endpoint != "https://tos-cn-beijing.volces.com" || cfg.Upload.Bucket != "llm-d1" {
		t.Errorf("unexpected TOS target: endpoint=%q bucket=%q", cfg.Upload.Endpoint, cfg.Upload.Bucket)
	}
	if !filepath.IsAbs(cfg.LogsRoot) || !filepath.IsAbs(cfg.WorkDir) {
		t.Errorf("config paths were not resolved relative to the config file: logs=%q work=%q", cfg.LogsRoot, cfg.WorkDir)
	}
}

func TestLoadConfigRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "log-uploader.yaml")
	if errWrite := os.WriteFile(path, []byte("unknown-setting: true\n"), 0o600); errWrite != nil {
		t.Fatalf("write invalid config: %v", errWrite)
	}
	_, errLoad := LoadConfig(path)
	if errLoad == nil || !strings.Contains(errLoad.Error(), "unknown-setting") {
		t.Fatalf("unknown config field error = %v", errLoad)
	}
}

func TestLoadConfigAcceptsLegacyModelAliases(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "log-uploader.yaml")
	if errWrite := os.WriteFile(path, []byte("model-aliases:\n  gpt-5.6-sol: codex56sol\n"), 0o600); errWrite != nil {
		t.Fatalf("write legacy config: %v", errWrite)
	}
	cfg, errLoad := LoadConfig(path)
	if errLoad != nil {
		t.Fatalf("load config with legacy model aliases: %v", errLoad)
	}
	if cfg.Models["gpt-5.6-sol"] != "codex56sol" {
		t.Errorf("legacy model alias was not retained for config compatibility")
	}
}
