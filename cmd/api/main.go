package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/stevians/wallet-service/internal/app"
	"github.com/stevians/wallet-service/internal/httpapi"
	"github.com/stevians/wallet-service/internal/idempotency"
	"github.com/stevians/wallet-service/internal/ledger"
	"github.com/stevians/wallet-service/internal/platform/bootstrap"
	"github.com/stevians/wallet-service/internal/platform/config"
	"github.com/stevians/wallet-service/internal/platform/db"
	"github.com/stevians/wallet-service/internal/platform/telemetry"
	"github.com/stevians/wallet-service/internal/reconciliation"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		bootstrap.Exit("load config", err)
	}
	proc, err := bootstrap.Start("wallet-api", cfg.LogLevel)
	if err != nil {
		bootstrap.Exit("start", err)
	}
	err = run(proc, cfg)
	proc.Close()
	if err != nil {
		bootstrap.Exit("api stopped", err)
	}
}

func run(proc *bootstrap.Process, cfg config.Config) error {
	ctx, logger := proc.Ctx, proc.Logger

	pool, err := db.OpenApp(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	telemetry.RegisterOutboxLag(pool)

	book := ledger.New(pool)
	idempotencyService := idempotency.New(pool, cfg.IdempotencyLease)
	reconService := reconciliation.NewService(pool)
	service := app.New(book, idempotencyService, reconService)
	handler := httpapi.New(service, logger, cfg.APIKey, cfg.AllowedOrigins, pool.Ping)

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http shutdown", "error", err)
		}
	}()

	logger.Info("http server listening", "addr", server.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}
