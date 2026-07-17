package loguploader

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config defines the standalone log uploader service.
type Config struct {
	LogsRoot  string          `yaml:"logs-root"`
	WorkDir   string          `yaml:"work-dir"`
	Timezone  string          `yaml:"timezone"`
	Schedule  ScheduleConfig  `yaml:"schedule"`
	Upload    UploadConfig    `yaml:"upload"`
	Retention RetentionConfig `yaml:"retention"`
	// Models is retained for backward-compatible config parsing.
	// Deprecated: Hourly archives always use the fixed archive name label.
	Models map[string]string `yaml:"model-aliases"`
}

type ScheduleConfig struct {
	Interval     time.Duration `yaml:"-"`
	IntervalRaw  string        `yaml:"interval"`
	RunOnStart   bool          `yaml:"run-on-start"`
	SettleDelay  time.Duration `yaml:"-"`
	SettleRaw    string        `yaml:"settle-delay"`
	CatchUpDelay time.Duration `yaml:"-"`
	CatchUpRaw   string        `yaml:"catch-up-delay"`
}

type UploadConfig struct {
	Enabled            bool   `yaml:"enabled"`
	Endpoint           string `yaml:"endpoint"`
	Region             string `yaml:"region"`
	Bucket             string `yaml:"bucket"`
	ObjectPrefix       string `yaml:"object-prefix"`
	AccessKeyIDEnv     string `yaml:"access-key-id-env"`
	SecretAccessKeyEnv string `yaml:"secret-access-key-env"`
	SessionTokenEnv    string `yaml:"session-token-env"`
}

type RetentionConfig struct {
	DeleteSourceAfterUpload bool `yaml:"delete-source-after-upload"`
	KeepLocalArchives       bool `yaml:"keep-local-archives"`
}

func LoadConfig(path string) (Config, error) {
	absolutePath, errAbsolute := filepath.Abs(path)
	if errAbsolute != nil {
		return Config{}, fmt.Errorf("resolve log uploader config path: %w", errAbsolute)
	}
	raw, errRead := os.ReadFile(absolutePath)
	if errRead != nil {
		return Config{}, fmt.Errorf("read log uploader config: %w", errRead)
	}

	cfg := Config{}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if errUnmarshal := decoder.Decode(&cfg); errUnmarshal != nil {
		return Config{}, fmt.Errorf("parse log uploader config: %w", errUnmarshal)
	}
	applyConfigDefaults(&cfg)
	resolveConfigPaths(&cfg, filepath.Dir(absolutePath))
	if errValidate := cfg.Validate(); errValidate != nil {
		return Config{}, errValidate
	}
	return cfg, nil
}

func resolveConfigPaths(cfg *Config, baseDir string) {
	if !filepath.IsAbs(cfg.LogsRoot) {
		cfg.LogsRoot = filepath.Join(baseDir, cfg.LogsRoot)
	}
	if !filepath.IsAbs(cfg.WorkDir) {
		cfg.WorkDir = filepath.Join(baseDir, cfg.WorkDir)
	}
}

func applyConfigDefaults(cfg *Config) {
	if strings.TrimSpace(cfg.LogsRoot) == "" {
		cfg.LogsRoot = filepath.Join("auths", "logs", "keys")
	}
	if strings.TrimSpace(cfg.WorkDir) == "" {
		cfg.WorkDir = filepath.Join("auths", "log-uploader")
	}
	if strings.TrimSpace(cfg.Timezone) == "" {
		cfg.Timezone = "Asia/Shanghai"
	}
	if strings.TrimSpace(cfg.Schedule.IntervalRaw) == "" {
		cfg.Schedule.IntervalRaw = "1h"
	}
	if strings.TrimSpace(cfg.Schedule.SettleRaw) == "" {
		cfg.Schedule.SettleRaw = "5m"
	}
	if strings.TrimSpace(cfg.Schedule.CatchUpRaw) == "" {
		cfg.Schedule.CatchUpRaw = "5m"
	}
	if strings.TrimSpace(cfg.Upload.Endpoint) == "" {
		cfg.Upload.Endpoint = "https://tos-cn-beijing.volces.com"
	}
	if strings.TrimSpace(cfg.Upload.Region) == "" {
		cfg.Upload.Region = "cn-beijing"
	}
	if strings.TrimSpace(cfg.Upload.ObjectPrefix) == "" {
		cfg.Upload.ObjectPrefix = "cliproxy-logs"
	}
	if strings.TrimSpace(cfg.Upload.AccessKeyIDEnv) == "" {
		cfg.Upload.AccessKeyIDEnv = "VOLC_TOS_ACCESS_KEY_ID"
	}
	if strings.TrimSpace(cfg.Upload.SecretAccessKeyEnv) == "" {
		cfg.Upload.SecretAccessKeyEnv = "VOLC_TOS_SECRET_ACCESS_KEY"
	}
	if strings.TrimSpace(cfg.Upload.SessionTokenEnv) == "" {
		cfg.Upload.SessionTokenEnv = "VOLC_TOS_SESSION_TOKEN"
	}
}

func (cfg *Config) Validate() error {
	interval, errInterval := time.ParseDuration(cfg.Schedule.IntervalRaw)
	if errInterval != nil || interval <= 0 {
		return fmt.Errorf("invalid schedule.interval %q", cfg.Schedule.IntervalRaw)
	}
	cfg.Schedule.Interval = interval

	settleDelay, errSettle := time.ParseDuration(cfg.Schedule.SettleRaw)
	if errSettle != nil || settleDelay < 0 {
		return fmt.Errorf("invalid schedule.settle-delay %q", cfg.Schedule.SettleRaw)
	}
	if settleDelay >= interval {
		return fmt.Errorf("schedule.settle-delay must be shorter than schedule.interval")
	}
	cfg.Schedule.SettleDelay = settleDelay

	catchUpDelay, errCatchUp := time.ParseDuration(cfg.Schedule.CatchUpRaw)
	if errCatchUp != nil || catchUpDelay <= 0 {
		return fmt.Errorf("invalid schedule.catch-up-delay %q", cfg.Schedule.CatchUpRaw)
	}
	cfg.Schedule.CatchUpDelay = catchUpDelay

	if _, errLocation := time.LoadLocation(cfg.Timezone); errLocation != nil {
		return fmt.Errorf("invalid timezone %q: %w", cfg.Timezone, errLocation)
	}
	if !cfg.Upload.Enabled {
		return nil
	}
	if strings.TrimSpace(cfg.Upload.Endpoint) == "" {
		return fmt.Errorf("upload.endpoint is required")
	}
	if strings.TrimSpace(cfg.Upload.Region) == "" {
		return fmt.Errorf("upload.region is required")
	}
	if strings.TrimSpace(cfg.Upload.Bucket) == "" {
		return fmt.Errorf("upload.bucket is required")
	}
	return nil
}
