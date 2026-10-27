package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/stevians/wallet-service/internal/outbox"
	"github.com/stevians/wallet-service/internal/platform/config"
	"github.com/stevians/wallet-service/internal/platform/db"
	"github.com/stevians/wallet-service/internal/platform/log"
	"github.com/stevians/wallet-service/internal/platform/telemetry"
)

func main() {
	cfg, err := config.LoadRelay()
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}

	logger := log.New(cfg.LogLevel)
	slog.SetDefault(logger)

	tracing, err := telemetry.InitTracing(context.Background(), "wallet-relay")
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

	publisher, err := outbox.NewKafkaPublisher(cfg.Brokers, cfg.Topic)
	if err != nil {
		logger.Error("create kafka publisher", "error", err)
		os.Exit(1)
	}
	defer publisher.Close()

	relay := outbox.NewRelay(outbox.NewStore(pool), publisher, logger, cfg.BatchSize, cfg.PollInterval, cfg.MaxBackoff)
	logger.Info("outbox relay started",
		"brokers", cfg.Brokers,
		"topic", cfg.Topic,
		"batch_size", cfg.BatchSize,
	)
	if err := relay.Run(ctx); err != nil {
		logger.Error("outbox relay stopped", "error", err)
		os.Exit(1)
	}
	logger.Info("outbox relay stopped")
}
