# ADR 0007: Observability and evidence architecture

## Status

Accepted.

## Context

The service needed end-to-end visibility for tracing, metrics exposure, and
load/chaos testing. Key decisions:

- Tracing: propagate W3C `traceparent` across HTTP → DB → outbox → Kafka → consumer.
- Metrics: Prometheus metrics exposed on `/metrics` (API only).
- Outbox lag: computed at scrape time from the DB.
- Load/chaos: k6 scripts + a single chaos script that orchestrates load and
  disruptions.

## Decisions

### Trace propagation

- `traceparent`/`tracestate` injected into `outbox_events.headers` JSON on insert
  (same transaction as the posting).
- Relay extracts the stored traceparent, starts a `kafka.publish` producer span
  as child of the request trace, and injects the producer span into the Kafka
  record headers.
- Consumer extracts `traceparent` from record headers and starts a
  `kafka.consume` consumer span.
- pgx `QueryTracer` implementation in `internal/platform/telemetry/pgx.go`
  emits DB client spans.

### Metrics

Exposed only by the API on `/metrics` (no auth). Counters/histograms:

- `wallet_http_request_duration_seconds{method,route,status}` histogram
- `wallet_http_requests_total{method,route,status}` counter
- `wallet_transfers_total{type,outcome}` counter
- `wallet_idempotency_replays_total{path}` counter
- `wallet_lock_wait_seconds{operation}` histogram (FOR UPDATE acquisition time)
- `wallet_outbox_lag_seconds` gauge (scrape-time DB query)

Outbox lag uses `prometheus.NewGaugeFunc` so the metric is always fresh and
works even when the relay is dead. Per-worker metrics are out of scope; the
required signals are all API-observable or DB-derived.

### Observability stack

Jaeger all-in-one (OTLP 4317, UI 16686), Prometheus (9090, scrape config in
`deploy/prometheus/prometheus.yml`), Grafana (3000, anonymous admin,
provisioned datasource + dashboard). All wired into docker-compose and started
by `make up`.

### Load tests

k6 script `deploy/k6/wallet.js`:
- `setup` creates 20 wallets and tops each up.
- Default iteration: 90% transfers between random wallets, 10% topups.
- `HOT=1`: 80% of transfers target one wallet (lock contention).
- Summary JSON exported to `results/summary.json`.

### Chaos orchestration

`scripts/chaos.sh` starts k6 and the relay itself, then:
1. Terminates Postgres backends (except own PID) via `pg_terminate_backend`.
2. Stops Kafka for 30s.
3. Kills the relay and restarts after 10s.
4. Waits for k6 to finish and outbox to drain.
5. Asserts `/v1/admin/integrity` returns `ok: true`.
6. Asserts posted transfer count equals published `transfer.posted` event count
   (single-statement snapshot).

The script is the single entry point for reproducible evidence.

## Consequences

- At-least-once relay can produce duplicate Kafka records when crash occurs
  between ProduceSync and commit. This is expected; consumers must dedupe.
- Killing Postgres backends causes expected 5xx during the chaos window.
- Lock wait histogram captures the time spent in FOR UPDATE, which is the
  dominant wait under hot-wallet contention.
- Outbox lag metric requires a DB query per Prometheus scrape (every 5s by
  default); negligible for dev but consider a cached approach in prod.
- Traces are not recorded if `OTEL_SDK_DISABLED=true` or
  `OTEL_EXPORTER_OTLP_ENDPOINT` is unset.
