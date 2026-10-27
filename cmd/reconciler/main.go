package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/stevians/wallet-service/internal/platform/config"
	"github.com/stevians/wallet-service/internal/platform/db"
	"github.com/stevians/wallet-service/internal/platform/log"
	"github.com/stevians/wallet-service/internal/platform/telemetry"
	"github.com/stevians/wallet-service/internal/reconciliation"
)

func main() {
	cfg, err := config.LoadReconciler()
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}

	logger := log.New(cfg.LogLevel)
	slog.SetDefault(logger)

	tracing, err := telemetry.InitTracing(context.Background(), "wallet-reconciler")
	if err != nil {
		logger.Error("init tracing", "error", err)
		os.Exit(1)
	}
	defer tracing.Shutdown(context.Background())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := db.VerifyAppRole(ctx, pool); err != nil {
		logger.Error("database role check failed", "error", err)
		os.Exit(1)
	}

	recon := reconciliation.NewService(pool)
	reconciler := recon.Reconciler()

	logger.Info("reconciler worker started", "poll_interval", cfg.PollInterval)

	for {
		select {
		case <-ctx.Done():
			logger.Info("reconciler worker shutting down")
			return
		default:
		}

		run, err := reconciler.ClaimRun(ctx)
		if err != nil {
			logger.Error("claim run", "error", err)
			time.Sleep(cfg.PollInterval)
			continue
		}

		if run == nil {
			time.Sleep(cfg.PollInterval)
			continue
		}

		logger.Info("claimed reconciliation run", "run_id", run.ID, "file_id", run.FileID)

		if err := reconciler.Execute(ctx, *run); err != nil {
			if errors.Is(err, ctx.Err()) {
				logger.Warn("reconciliation run interrupted, leaving status=running for retry",
					"run_id", run.ID)
				continue
			}
			logger.Error("execute reconciliation", "run_id", run.ID, "error", err)
			if markErr := reconciler.MarkFailed(ctx, run.ID, err.Error()); markErr != nil {
				logger.Error("mark run failed", "run_id", run.ID, "error", markErr)
			}
			continue
		}

		logger.Info("reconciliation run completed", "run_id", run.ID)
	}
}
