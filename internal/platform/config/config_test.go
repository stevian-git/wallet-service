package config

import (
	"testing"
	"time"
)

func TestLoadRelayDefaultsAndValidation(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("KAFKA_BROKERS", " a:1, ,b:2 ")

	cfg, err := LoadRelay()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Topic != "ledger.events.v1" || cfg.BatchSize != 50 ||
		cfg.PollInterval != time.Second || cfg.MaxBackoff != 30*time.Second ||
		len(cfg.Brokers) != 2 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}

	t.Setenv("OUTBOX_POLL_INTERVAL", "-1s")
	if _, err := LoadRelay(); err == nil || err.Error() != "OUTBOX_POLL_INTERVAL must be a positive duration" {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadAPIRequiresDatabaseURLAndValidatesLease(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	if _, err := Load(); err == nil || err.Error() != "DATABASE_URL is required and must use the ledger_app role" {
		t.Fatalf("err = %v", err)
	}

	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("API_KEY", "k")
	t.Setenv("IDEMPOTENCY_LEASE_SECONDS", "0")
	if _, err := Load(); err == nil || err.Error() != "IDEMPOTENCY_LEASE_SECONDS must be a positive integer" {
		t.Fatalf("err = %v", err)
	}

	t.Setenv("IDEMPOTENCY_LEASE_SECONDS", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != "8080" || cfg.IdempotencyLease != 30*time.Second {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadAPIAllowedOrigins(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("API_KEY", "k")

	t.Setenv("CORS_ALLOWED_ORIGINS", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AllowedOrigins) != 1 || cfg.AllowedOrigins[0] != "http://localhost:5173" {
		t.Fatalf("default AllowedOrigins = %q", cfg.AllowedOrigins)
	}

	t.Setenv("CORS_ALLOWED_ORIGINS", " https://a.example , ,https://b.example ")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AllowedOrigins) != 2 || cfg.AllowedOrigins[0] != "https://a.example" || cfg.AllowedOrigins[1] != "https://b.example" {
		t.Fatalf("AllowedOrigins = %q", cfg.AllowedOrigins)
	}
}

func TestLoadToolNeedsOnlyDatabaseURL(t *testing.T) {
	t.Setenv("API_KEY", "")
	t.Setenv("DATABASE_URL", "")
	if _, err := LoadTool(); err == nil {
		t.Fatal("LoadTool without DATABASE_URL succeeded")
	}
	t.Setenv("DATABASE_URL", "postgres://x")
	cfg, err := LoadTool()
	if err != nil {
		t.Fatalf("LoadTool without API_KEY: %v", err)
	}
	if cfg.DatabaseURL != "postgres://x" {
		t.Fatalf("DatabaseURL = %q", cfg.DatabaseURL)
	}
}
