package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
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
		run, err := reconciler.ClaimRun(ctx)
		if err != nil {
			if ctx.Err() == nil {
				logger.Error("claim run", "error", err)
			}
			if !sleep(ctx, cfg.PollInterval) {
				break
			}
			continue
		}
		if run == nil {
			if !sleep(ctx, cfg.PollInterval) {
				break
			}
			continue
		}

		logger.Info("claimed reconciliation run", "run_id", run.ID, "file_id", run.FileID)

		if err := reconciler.Execute(ctx, *run); err != nil {
			switch {
			case errors.Is(err, ctx.Err()):
				logger.Warn("reconciliation run interrupted, leaving status=running for retry",
					"run_id", run.ID)
			case errors.Is(err, reconciliation.ErrRunBusy), errors.Is(err, reconciliation.ErrRunNotRunning):
				// Another worker took or finished this run between claim and execute.
				logger.Debug("reconciliation run skipped", "run_id", run.ID, "reason", err)
			default:
				logger.Error("execute reconciliation", "run_id", run.ID, "error", err)
				markFailed(ctx, logger, reconciler, run.ID, err)
			}
			continue
		}

		logger.Info("reconciliation run completed", "run_id", run.ID)
	}
	logger.Info("reconciler worker shutting down")
}

// markFailed records the failure even while shutting down, so a run that failed
// for a real reason is not retried forever after a restart.
func markFailed(ctx context.Context, logger *slog.Logger, reconciler *reconciliation.Reconciler, runID uuid.UUID, cause error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := reconciler.MarkFailed(ctx, runID, cause.Error()); err != nil {
		logger.Error("mark run failed", "run_id", runID, "error", err)
	}
}

// sleep waits for d or until ctx is cancelled; it reports whether to keep going.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
