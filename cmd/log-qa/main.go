// Package main provides the standalone request log QA service.
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
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logqa"
	log "github.com/sirupsen/logrus"
)

func main() {
	logging.SetupBaseLogger()
	if err := run(); err != nil {
		log.WithError(err).Error("log-qa stopped with an error")
		os.Exit(1)
	}
}

func run() error {
	var configPath string
	var once bool
	flag.StringVar(&configPath, "config", "log-qa.yaml", "Path to the log QA YAML configuration")
	flag.BoolVar(&once, "once", false, "Run a single QA pass and exit")
	flag.Parse()

	absoluteConfigPath, errAbs := filepath.Abs(configPath)
	if errAbs != nil {
		return fmt.Errorf("resolve config path: %w", errAbs)
	}
	if errEnv := godotenv.Load(filepath.Join(filepath.Dir(absoluteConfigPath), ".env")); errEnv != nil && !os.IsNotExist(errEnv) {
		log.WithError(errEnv).Warn("failed to load .env")
	}

	cfg, errConfig := logqa.LoadConfig(absoluteConfigPath)
	if errConfig != nil {
		return errConfig
	}
	service := logqa.NewService(cfg)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if once {
		log.WithFields(log.Fields{
			"logs_root": cfg.LogsRoot,
			"work_dir":  cfg.WorkDir,
		}).Info("log-qa running once")
		return service.RunOnce(ctx)
	}

	log.WithFields(log.Fields{
		"interval":      cfg.Schedule.Interval.String(),
		"initial_delay": cfg.Schedule.InitialDelay.String(),
		"logs_root":     cfg.LogsRoot,
		"work_dir":      cfg.WorkDir,
	}).Info("log-qa service started")
	return service.Run(ctx)
}
