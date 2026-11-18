// Package bootstrap is the startup every long-running binary shares: a JSON
// logger set as the slog default, tracing, and a context cancelled on
// SIGINT/SIGTERM. Each cmd/*/main.go does its own wiring on top of it.
package bootstrap

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/stevians/wallet-service/internal/platform/log"
	"github.com/stevians/wallet-service/internal/platform/telemetry"
)

// tracingShutdownTimeout bounds the final span flush, so an unreachable
// collector cannot keep a stopping process alive.
const tracingShutdownTimeout = 5 * time.Second

type Process struct {
	// Ctx is cancelled on SIGINT or SIGTERM.
	Ctx     context.Context
	Logger  *slog.Logger
	stop    context.CancelFunc
	tracing *telemetry.Tracing
}

// Start sets up logging, tracing for serviceName, and signal handling.
func Start(serviceName string, level slog.Level) (*Process, error) {
	logger := log.New(level)
	slog.SetDefault(logger)

	tracing, err := telemetry.InitTracing(context.Background(), serviceName)
	if err != nil {
		return nil, err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	return &Process{Ctx: ctx, Logger: logger, stop: stop, tracing: tracing}, nil
}

// Close flushes traces and stops listening for signals.
func (p *Process) Close() {
	p.stop()
	ctx, cancel := context.WithTimeout(context.Background(), tracingShutdownTimeout)
	defer cancel()
	if err := p.tracing.Shutdown(ctx); err != nil {
		p.Logger.Error("shutdown tracing", "error", err)
	}
}

// Exit logs a fatal error and exits with status 1. Only main calls it, after
// run has returned, so every deferred cleanup in run has already happened.
func Exit(msg string, err error) {
	slog.Error(msg, "error", err)
	os.Exit(1)
}
