package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port             string
	DatabaseURL      string
	APIKey           string
	LogLevel         slog.Level
	IdempotencyLease time.Duration
}

type HoldSweeperConfig struct {
	DatabaseURL string
	LogLevel    slog.Level
	Interval    time.Duration
	BatchSize   int
}

type RelayConfig struct {
	DatabaseURL  string
	LogLevel     slog.Level
	Brokers      []string
	Topic        string
	BatchSize    int
	PollInterval time.Duration
	MaxBackoff   time.Duration
}

type ConsumerConfig struct {
	LogLevel slog.Level
	Brokers  []string
	Topic    string
	Group    string
}

func Load() (Config, error) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required and must use the ledger_app role")
	}

	apiKey := os.Getenv("API_KEY")
	if apiKey == "" {
		return Config{}, fmt.Errorf("API_KEY is required")
	}

	port := os.Getenv("APP_PORT")
	if port == "" {
		port = "8080"
	}

	level, err := parseLogLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		return Config{}, err
	}

	lease := 30 * time.Second
	if raw := os.Getenv("IDEMPOTENCY_LEASE_SECONDS"); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds <= 0 {
			return Config{}, fmt.Errorf("IDEMPOTENCY_LEASE_SECONDS must be a positive integer")
		}
		lease = time.Duration(seconds) * time.Second
	}

	return Config{
		Port:             port,
		DatabaseURL:      databaseURL,
		APIKey:           apiKey,
		LogLevel:         level,
		IdempotencyLease: lease,
	}, nil
}

func LoadHoldSweeper() (HoldSweeperConfig, error) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return HoldSweeperConfig{}, fmt.Errorf("DATABASE_URL is required and must use the ledger_app role")
	}
	level, err := parseLogLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		return HoldSweeperConfig{}, err
	}
	interval := 5 * time.Second
	if raw := os.Getenv("HOLD_SWEEP_INTERVAL"); raw != "" {
		interval, err = time.ParseDuration(raw)
		if err != nil || interval <= 0 {
			return HoldSweeperConfig{}, fmt.Errorf("HOLD_SWEEP_INTERVAL must be a positive duration")
		}
	}
	batchSize := 100
	if raw := os.Getenv("HOLD_SWEEP_BATCH_SIZE"); raw != "" {
		batchSize, err = strconv.Atoi(raw)
		if err != nil || batchSize <= 0 {
			return HoldSweeperConfig{}, fmt.Errorf("HOLD_SWEEP_BATCH_SIZE must be a positive integer")
		}
	}
	return HoldSweeperConfig{
		DatabaseURL: databaseURL,
		LogLevel:    level,
		Interval:    interval,
		BatchSize:   batchSize,
	}, nil
}

func LoadRelay() (RelayConfig, error) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return RelayConfig{}, fmt.Errorf("DATABASE_URL is required and must use the ledger_app role")
	}
	brokers, err := kafkaBrokers()
	if err != nil {
		return RelayConfig{}, err
	}
	level, err := parseLogLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		return RelayConfig{}, err
	}
	topic := os.Getenv("KAFKA_TOPIC")
	if topic == "" {
		topic = "ledger.events.v1"
	}
	batchSize := 50
	if raw := os.Getenv("OUTBOX_BATCH_SIZE"); raw != "" {
		batchSize, err = strconv.Atoi(raw)
		if err != nil || batchSize <= 0 {
			return RelayConfig{}, fmt.Errorf("OUTBOX_BATCH_SIZE must be a positive integer")
		}
	}
	pollInterval := time.Second
	if raw := os.Getenv("OUTBOX_POLL_INTERVAL"); raw != "" {
		pollInterval, err = time.ParseDuration(raw)
		if err != nil || pollInterval <= 0 {
			return RelayConfig{}, fmt.Errorf("OUTBOX_POLL_INTERVAL must be a positive duration")
		}
	}
	maxBackoff := 30 * time.Second
	if raw := os.Getenv("OUTBOX_MAX_BACKOFF"); raw != "" {
		maxBackoff, err = time.ParseDuration(raw)
		if err != nil || maxBackoff <= 0 {
			return RelayConfig{}, fmt.Errorf("OUTBOX_MAX_BACKOFF must be a positive duration")
		}
	}
	return RelayConfig{
		DatabaseURL:  databaseURL,
		LogLevel:     level,
		Brokers:      brokers,
		Topic:        topic,
		BatchSize:    batchSize,
		PollInterval: pollInterval,
		MaxBackoff:   maxBackoff,
	}, nil
}

func LoadConsumer() (ConsumerConfig, error) {
	brokers, err := kafkaBrokers()
	if err != nil {
		return ConsumerConfig{}, err
	}
	level, err := parseLogLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		return ConsumerConfig{}, err
	}
	topic := os.Getenv("KAFKA_TOPIC")
	if topic == "" {
		topic = "ledger.events.v1"
	}
	group := os.Getenv("KAFKA_CONSUMER_GROUP")
	if group == "" {
		group = "ledger-consumer-demo"
	}
	return ConsumerConfig{LogLevel: level, Brokers: brokers, Topic: topic, Group: group}, nil
}

type ReconcilerConfig struct {
	DatabaseURL  string
	LogLevel     slog.Level
	PollInterval time.Duration
}

func LoadReconciler() (ReconcilerConfig, error) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return ReconcilerConfig{}, fmt.Errorf("DATABASE_URL is required and must use the ledger_app role")
	}
	level, err := parseLogLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		return ReconcilerConfig{}, err
	}
	pollInterval := 5 * time.Second
	if raw := os.Getenv("RECONCILER_POLL_INTERVAL"); raw != "" {
		pollInterval, err = time.ParseDuration(raw)
		if err != nil || pollInterval <= 0 {
			return ReconcilerConfig{}, fmt.Errorf("RECONCILER_POLL_INTERVAL must be a positive duration")
		}
	}
	return ReconcilerConfig{
		DatabaseURL:  databaseURL,
		LogLevel:     level,
		PollInterval: pollInterval,
	}, nil
}

func kafkaBrokers() ([]string, error) {
	raw := strings.TrimSpace(os.Getenv("KAFKA_BROKERS"))
	if raw == "" {
		return nil, fmt.Errorf("KAFKA_BROKERS is required (comma-separated host:port list)")
	}
	parts := strings.Split(raw, ",")
	brokers := make([]string, 0, len(parts))
	for _, part := range parts {
		broker := strings.TrimSpace(part)
		if broker != "" {
			brokers = append(brokers, broker)
		}
	}
	if len(brokers) == 0 {
		return nil, fmt.Errorf("KAFKA_BROKERS must contain at least one host:port")
	}
	return brokers, nil
}

func parseLogLevel(raw string) (slog.Level, error) {
	switch raw {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("LOG_LEVEL must be debug, info, warn, or error")
	}
}
