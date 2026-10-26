package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Port             string
	DatabaseURL      string
	APIKey           string
	LogLevel         slog.Level
	IdempotencyLease time.Duration
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
