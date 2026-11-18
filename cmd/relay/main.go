package main

import (
	"fmt"

	"github.com/stevians/wallet-service/internal/outbox"
	"github.com/stevians/wallet-service/internal/platform/bootstrap"
	"github.com/stevians/wallet-service/internal/platform/config"
	"github.com/stevians/wallet-service/internal/platform/db"
)

func main() {
	cfg, err := config.LoadRelay()
	if err != nil {
		bootstrap.Exit("load config", err)
	}
	proc, err := bootstrap.Start("wallet-relay", cfg.LogLevel)
	if err != nil {
		bootstrap.Exit("start", err)
	}
	err = run(proc, cfg)
	proc.Close()
	if err != nil {
		bootstrap.Exit("outbox relay stopped", err)
	}
}

func run(proc *bootstrap.Process, cfg config.RelayConfig) error {
	ctx, logger := proc.Ctx, proc.Logger

	pool, err := db.OpenApp(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	publisher, err := outbox.NewKafkaPublisher(cfg.Brokers, cfg.Topic)
	if err != nil {
		return fmt.Errorf("create kafka publisher: %w", err)
	}
	defer publisher.Close()

	relay := outbox.NewRelay(outbox.NewStore(pool), publisher, logger, cfg.BatchSize, cfg.PollInterval, cfg.MaxBackoff)
	logger.Info("outbox relay started",
		"brokers", cfg.Brokers,
		"topic", cfg.Topic,
		"batch_size", cfg.BatchSize,
	)
	if err := relay.Run(ctx); err != nil {
		return err
	}
	logger.Info("outbox relay stopped")
	return nil
}
