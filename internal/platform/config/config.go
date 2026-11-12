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
	databaseURL, err := requireDatabaseURL()
	if err != nil {
		return Config{}, err
	}

	apiKey := os.Getenv("API_KEY")
	if apiKey == "" {
		return Config{}, fmt.Errorf("API_KEY is required")
	}

	port := envOr("APP_PORT", "8080")

	level, err := logLevel()
	if err != nil {
		return Config{}, err
	}

	leaseSeconds, err := envPositiveInt("IDEMPOTENCY_LEASE_SECONDS", 30)
	if err != nil {
		return Config{}, err
	}
	lease := time.Duration(leaseSeconds) * time.Second

	return Config{
		Port:             port,
		DatabaseURL:      databaseURL,
		APIKey:           apiKey,
		LogLevel:         level,
		IdempotencyLease: lease,
	}, nil
}

func LoadHoldSweeper() (HoldSweeperConfig, error) {
	databaseURL, err := requireDatabaseURL()
	if err != nil {
		return HoldSweeperConfig{}, err
	}
	level, err := logLevel()
	if err != nil {
		return HoldSweeperConfig{}, err
	}
	interval, err := envPositiveDuration("HOLD_SWEEP_INTERVAL", 5*time.Second)
	if err != nil {
		return HoldSweeperConfig{}, err
	}
	batchSize, err := envPositiveInt("HOLD_SWEEP_BATCH_SIZE", 100)
	if err != nil {
		return HoldSweeperConfig{}, err
	}
	return HoldSweeperConfig{
		DatabaseURL: databaseURL,
		LogLevel:    level,
		Interval:    interval,
		BatchSize:   batchSize,
	}, nil
}

func LoadRelay() (RelayConfig, error) {
	databaseURL, err := requireDatabaseURL()
	if err != nil {
		return RelayConfig{}, err
	}
	brokers, err := kafkaBrokers()
	if err != nil {
		return RelayConfig{}, err
	}
	level, err := logLevel()
	if err != nil {
		return RelayConfig{}, err
	}
	topic := envOr("KAFKA_TOPIC", "ledger.events.v1")
	batchSize, err := envPositiveInt("OUTBOX_BATCH_SIZE", 50)
	if err != nil {
		return RelayConfig{}, err
	}
	pollInterval, err := envPositiveDuration("OUTBOX_POLL_INTERVAL", time.Second)
	if err != nil {
		return RelayConfig{}, err
	}
	maxBackoff, err := envPositiveDuration("OUTBOX_MAX_BACKOFF", 30*time.Second)
	if err != nil {
		return RelayConfig{}, err
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
	level, err := logLevel()
	if err != nil {
		return ConsumerConfig{}, err
	}
	topic := envOr("KAFKA_TOPIC", "ledger.events.v1")
	group := envOr("KAFKA_CONSUMER_GROUP", "ledger-consumer-demo")
	return ConsumerConfig{LogLevel: level, Brokers: brokers, Topic: topic, Group: group}, nil
}

type ReconcilerConfig struct {
	DatabaseURL  string
	LogLevel     slog.Level
	PollInterval time.Duration
}

func LoadReconciler() (ReconcilerConfig, error) {
	databaseURL, err := requireDatabaseURL()
	if err != nil {
		return ReconcilerConfig{}, err
	}
	level, err := logLevel()
	if err != nil {
		return ReconcilerConfig{}, err
	}
	pollInterval, err := envPositiveDuration("RECONCILER_POLL_INTERVAL", 5*time.Second)
	if err != nil {
		return ReconcilerConfig{}, err
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

func requireDatabaseURL() (string, error) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return "", fmt.Errorf("DATABASE_URL is required and must use the ledger_app role")
	}
	return databaseURL, nil
}

func logLevel() (slog.Level, error) {
	return parseLogLevel(os.Getenv("LOG_LEVEL"))
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envPositiveInt(name string, fallback int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}

func envPositiveDuration(name string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return value, nil
}
