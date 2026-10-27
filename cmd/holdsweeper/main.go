package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/stevians/wallet-service/internal/ledger"
	"github.com/stevians/wallet-service/internal/platform/config"
	"github.com/stevians/wallet-service/internal/platform/db"
	"github.com/stevians/wallet-service/internal/platform/log"
	"github.com/stevians/wallet-service/internal/platform/telemetry"
)

func main() {
	cfg, err := config.LoadHoldSweeper()
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}

	logger := log.New(cfg.LogLevel)
	slog.SetDefault(logger)

	tracing, err := telemetry.InitTracing(context.Background(), "wallet-holdsweeper")
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

	book := ledger.New(pool)
	logger.Info("hold sweeper started", "interval", cfg.Interval, "batch_size", cfg.BatchSize)
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	for {
		sweep(ctx, book, logger, cfg.BatchSize)
		select {
		case <-ctx.Done():
			logger.Info("hold sweeper stopped")
			return
		case <-ticker.C:
		}
	}
}

func sweep(ctx context.Context, book *ledger.Ledger, logger *slog.Logger, batchSize int) {
	for i := 0; i < batchSize; i++ {
		hold, ok, err := book.ExpireOne(ctx)
		if err != nil {
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				logger.Error("expire hold", "error", err)
			}
			return
		}
		if !ok {
			return
		}
		logger.Info("hold expired", "hold_id", hold.ID, "account_id", hold.AccountID, "amount", hold.Amount)
	}
}
