package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/stevians/wallet-service/internal/consumer"
	"github.com/stevians/wallet-service/internal/platform/config"
	"github.com/stevians/wallet-service/internal/platform/log"
)

func main() {
	cfg, err := config.LoadConsumer()
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}

	logger := log.New(cfg.LogLevel)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client, err := kgo.NewClient(
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ConsumerGroup(cfg.Group),
		kgo.ConsumeTopics(cfg.Topic),
		kgo.DisableAutoCommit(),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.ClientID("wallet-consumer-demo"),
	)
	if err != nil {
		logger.Error("create kafka consumer", "error", err)
		os.Exit(1)
	}
	defer client.Close()

	dedupe := consumer.NewDedupe(24*time.Hour, 100_000)
	logger.Info("consumer started",
		"brokers", cfg.Brokers,
		"topic", cfg.Topic,
		"group", cfg.Group,
		"dedupe_scope", "in-memory (demo only)",
	)

	for {
		fetches := client.PollFetches(ctx)
		if ctx.Err() != nil {
			break
		}
		for _, fetchErr := range fetches.Errors() {
			logger.Error("kafka fetch failed",
				"topic", fetchErr.Topic,
				"partition", fetchErr.Partition,
				"error", fetchErr.Err,
			)
		}

		fetches.EachRecord(func(record *kgo.Record) {
			eventID := headerValue(record.Headers, "event_id")
			if eventID == "" {
				logger.Warn("event without event_id skipped",
					"topic", record.Topic,
					"partition", record.Partition,
					"offset", record.Offset,
				)
				return
			}
			if dedupe.Seen(eventID) {
				logger.Info("duplicate event skipped",
					"event_id", eventID,
					"topic", record.Topic,
					"partition", record.Partition,
					"offset", record.Offset,
				)
				return
			}
			dedupe.Add(eventID)
			logger.Info("event received",
				"event_id", eventID,
				"key", string(record.Key),
				"topic", record.Topic,
				"partition", record.Partition,
				"offset", record.Offset,
				"payload", string(record.Value),
			)
		})

		if err := client.CommitUncommittedOffsets(ctx); err != nil {
			if ctx.Err() != nil {
				break
			}
			logger.Error("commit kafka offsets", "error", err)
		}
	}
	logger.Info("consumer stopped")
}

func headerValue(headers []kgo.RecordHeader, key string) string {
	for _, header := range headers {
		if header.Key == key {
			return string(header.Value)
		}
	}
	return ""
}
