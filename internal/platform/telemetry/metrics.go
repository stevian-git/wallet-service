package telemetry

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	httpRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "wallet_http_request_duration_seconds",
		Help:    "HTTP request latency in seconds.",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"method", "route", "status"})

	httpRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "wallet_http_requests_total",
		Help: "Total HTTP requests.",
	}, []string{"method", "route", "status"})

	transfersTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "wallet_transfers_total",
		Help: "Transfer operations by type and outcome.",
	}, []string{"type", "outcome"})

	idempotencyReplays = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "wallet_idempotency_replays_total",
		Help: "Idempotency replays by request path.",
	}, []string{"path"})

	lockWait = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "wallet_lock_wait_seconds",
		Help:    "Time spent acquiring FOR UPDATE row locks.",
		Buckets: []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
	}, []string{"operation"})

	lagRegisterOnce sync.Once
)

func RecordHTTP(method, route, status string, duration time.Duration) {
	httpRequestDuration.WithLabelValues(method, route, status).Observe(duration.Seconds())
	httpRequestsTotal.WithLabelValues(method, route, status).Inc()
}

func RecordTransfer(transferType, outcome string) {
	transfersTotal.WithLabelValues(transferType, outcome).Inc()
}

func RecordReplay(path string) {
	idempotencyReplays.WithLabelValues(path).Inc()
}

func ObserveLockWait(operation string, duration time.Duration) {
	lockWait.WithLabelValues(operation).Observe(duration.Seconds())
}

func RegisterOutboxLag(pool *pgxpool.Pool) {
	if pool == nil {
		return
	}
	lagRegisterOnce.Do(func() {
		gf := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "wallet_outbox_lag_seconds",
			Help: "Age of the oldest unpublished outbox event in seconds (computed at scrape time).",
		}, func() float64 {
			if pool == nil {
				return math.NaN()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			var seconds float64
			if err := pool.QueryRow(ctx, `
				SELECT COALESCE(EXTRACT(EPOCH FROM (now() - MIN(created_at))), 0)
				FROM outbox_events WHERE published_at IS NULL
			`).Scan(&seconds); err != nil {
				return math.NaN()
			}
			return seconds
		})
		prometheus.MustRegister(gf)
	})
}

func HTTPRequestDuration() *prometheus.HistogramVec { return httpRequestDuration }
func HTTPRequestsTotal() *prometheus.CounterVec     { return httpRequestsTotal }
func TransfersTotal() *prometheus.CounterVec        { return transfersTotal }
func IdempotencyReplays() *prometheus.CounterVec    { return idempotencyReplays }
func LockWait() *prometheus.HistogramVec            { return lockWait }
