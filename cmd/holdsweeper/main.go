package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/stevians/wallet-service/internal/ledger"
	"github.com/stevians/wallet-service/internal/platform/bootstrap"
	"github.com/stevians/wallet-service/internal/platform/config"
	"github.com/stevians/wallet-service/internal/platform/db"
)

func main() {
	cfg, err := config.LoadHoldSweeper()
	if err != nil {
		bootstrap.Exit("load config", err)
	}
	proc, err := bootstrap.Start("wallet-holdsweeper", cfg.LogLevel)
	if err != nil {
		bootstrap.Exit("start", err)
	}
	err = run(proc, cfg)
	proc.Close()
	if err != nil {
		bootstrap.Exit("hold sweeper stopped", err)
	}
}

func run(proc *bootstrap.Process, cfg config.HoldSweeperConfig) error {
	ctx, logger := proc.Ctx, proc.Logger

	pool, err := db.OpenApp(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	book := ledger.New(pool)
	logger.Info("hold sweeper started", "interval", cfg.Interval, "batch_size", cfg.BatchSize)
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	for {
		sweep(ctx, book, logger, cfg.BatchSize)
		select {
		case <-ctx.Done():
			logger.Info("hold sweeper stopped")
			return nil
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
