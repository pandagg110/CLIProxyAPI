// Package main provides the standalone request log uploader service.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/joho/godotenv"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/loguploader"
	log "github.com/sirupsen/logrus"
)

func main() {
	logging.SetupBaseLogger()
	if errRun := run(); errRun != nil {
		log.WithError(errRun).Error("log uploader stopped with an error")
		os.Exit(1)
	}
}

func run() error {
	var configPath string
	var once bool
	var dryRun bool
	var migrateManifest string
	var migrateArchives string
	var migrateTrustLocal bool
	flag.StringVar(&configPath, "config", "log-uploader.yaml", "Path to the log uploader YAML configuration")
	flag.BoolVar(&once, "once", false, "Process ready logs once and exit")
	flag.BoolVar(&dryRun, "dry-run", false, "Build local archives without uploading, recording state, or deleting source logs")
	flag.StringVar(&migrateManifest, "migrate-legacy-manifest", "", "Verified JSONL manifest used to migrate an untrusted legacy state")
	flag.StringVar(&migrateArchives, "migrate-legacy-archives", "", "Root containing verified local archives for legacy state migration")
	flag.BoolVar(&migrateTrustLocal, "migrate-legacy-trust-local", false, "Migrate using verified local archives and upload audit when HeadObject permission is unavailable")
	flag.Parse()

	absoluteConfigPath, errAbsoluteConfig := filepath.Abs(configPath)
	if errAbsoluteConfig != nil {
		return fmt.Errorf("resolve config path: %w", errAbsoluteConfig)
	}
	if errEnv := godotenv.Load(filepath.Join(filepath.Dir(absoluteConfigPath), ".env")); errEnv != nil && !os.IsNotExist(errEnv) {
		log.WithError(errEnv).Warn("failed to load .env")
	}

	cfg, errConfig := loguploader.LoadConfig(absoluteConfigPath)
	if errConfig != nil {
		return errConfig
	}
	if !cfg.Upload.Enabled && !dryRun {
		return fmt.Errorf("upload is disabled; use --dry-run for local conversion testing")
	}

	var uploader loguploader.ObjectUploader
	if cfg.Upload.Enabled && !dryRun {
		tosUploader, errUploader := loguploader.NewTOSUploader(cfg.Upload)
		if errUploader != nil {
			return errUploader
		}
		uploader = tosUploader
	}
	service, errService := loguploader.NewService(cfg, uploader)
	if errService != nil {
		return errService
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if migrateManifest != "" || migrateArchives != "" || migrateTrustLocal {
		if migrateManifest == "" || migrateArchives == "" {
			return fmt.Errorf("both --migrate-legacy-manifest and --migrate-legacy-archives are required")
		}
		if dryRun || once {
			return fmt.Errorf("legacy migration flags cannot be combined with --once or --dry-run")
		}
		return service.MigrateLegacyState(ctx, migrateManifest, migrateArchives, migrateTrustLocal)
	}
	if once {
		return service.RunOnce(ctx, dryRun)
	}
	log.WithFields(log.Fields{
		"interval":  cfg.Schedule.Interval,
		"logs_root": cfg.LogsRoot,
		"upload":    cfg.Upload.Enabled && !dryRun,
	}).Info("log uploader service started")
	return service.Run(ctx, dryRun)
}
