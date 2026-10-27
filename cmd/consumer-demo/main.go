package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/stevians/wallet-service/internal/consumer"
	"github.com/stevians/wallet-service/internal/platform/config"
	"github.com/stevians/wallet-service/internal/platform/log"
	"github.com/stevians/wallet-service/internal/platform/telemetry"
)

func main() {
	cfg, err := config.LoadConsumer()
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}

	logger := log.New(cfg.LogLevel)
	slog.SetDefault(logger)

	tracing, err := telemetry.InitTracing(context.Background(), "wallet-consumer-demo")
	if err != nil {
		logger.Error("init tracing", "error", err)
		os.Exit(1)
	}
	defer tracing.Shutdown(context.Background())

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

			headers := make(map[string]string, len(record.Headers))
			for _, h := range record.Headers {
				headers[h.Key] = string(h.Value)
			}
			consumeCtx := telemetry.ExtractTrace(ctx, headers)
			_, span := otel.Tracer("wallet.kafka").Start(consumeCtx, "kafka.consume",
				trace.WithSpanKind(trace.SpanKindConsumer),
				trace.WithAttributes(
					attribute.String("messaging.system", "kafka"),
					attribute.String("messaging.destination.name", record.Topic),
					attribute.Int64("messaging.kafka.partition", int64(record.Partition)),
					attribute.Int64("messaging.kafka.offset", record.Offset),
					attribute.String("wallet.event_id", eventID),
				),
			)

			logger.Info("event received",
				"event_id", eventID,
				"key", string(record.Key),
				"topic", record.Topic,
				"partition", record.Partition,
				"offset", record.Offset,
				"payload", string(record.Value),
			)

			span.End()
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
