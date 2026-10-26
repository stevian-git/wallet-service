# Project: Mini Core Banking / Wallet Ledger (Go)

## Goal
A portfolio-grade wallet service built on a double-entry ledger, targeting fintech
backend interviews. Correctness and demonstrable failure handling matter more than
feature count or UI polish.

## Source of truth
- docs/schema.sql  : database schema. Do NOT change it without asking me first. If you
                     think a change is needed, explain why and propose a new migration.
- docs/openapi.yaml: HTTP contract. Implement it as written. Propose changes before
                     deviating.

## Non-negotiable invariants (never violate, never "simplify" away)
1. Money is int64 minor units. Never float64, never string math, never decimal for balances.
2. ledger_entries is append-only. Corrections are new reversing transfers, never UPDATE/DELETE.
   Enforced by DB privileges: the application role (ledger_app) has only SELECT and INSERT
   on ledger_entries and audit_log. The app must never connect as the table owner.
3. Every transfer is balanced (sum debit == sum credit). Enforce it in ONE place: the ledger
   Post() function rejects an unbalanced set of entries before touching the DB. There is no
   DB trigger for this by design. Correctness is verified by the v_unbalanced_transfers view,
   GET /v1/admin/integrity, and property tests.
4. Balance cache (account_balances) changes in the SAME DB transaction as the ledger entries.
5. Any operation touching multiple accounts locks account_balances rows in a
   deterministic order (ORDER BY account_id) using SELECT ... FOR UPDATE, to prevent deadlocks.
6. Every money-moving endpoint is idempotent via the Idempotency-Key header
   (same key + same body = same response; same key + different body = 422;
   in-flight duplicate = 409). The idempotency record and the ledger write commit atomically.
7. Events are published ONLY through the transactional outbox (outbox_events row inserted in the
   same transaction). Never publish to Kafka directly from a request handler (no dual write).
8. Consumers and the outbox relay are at-least-once; consumers must be idempotent.
9. No business logic in HTTP handlers. Handlers parse/validate/map errors only.
10. No triggers, stored functions, or stored procedures. The database has only declarative
    constraints (PK/FK/UNIQUE/CHECK), read-only views, and GRANTs. All business logic lives
    in Go. Migrations must be runnable by a non-superuser role (ledger_migrator).
11. Insufficient funds is decided in Go: after locking the account_balances rows, compare the
    available balance (posted - held) and return ErrInsufficientFunds. The CHECK on
    account_balances is only a backstop; if SQLSTATE 23514 ever fires, log it as an error
    (it means a bug or a missed race), then map it to ErrInsufficientFunds.

## Stack
- Go 1.23+, chi router, pgx v5 (pgxpool), sqlc for queries, golang-migrate for migrations
- Kafka via franz-go (or segmentio/kafka-go), local broker via docker compose (Redpanda is fine)
- slog for logging (JSON), OpenTelemetry for traces, Prometheus for metrics
- Tests: testify + testcontainers-go (real Postgres/Kafka), no mocked database for ledger tests

## Repo layout
cmd/api            HTTP server
cmd/relay          outbox relay (polls outbox_events, publishes to Kafka)
cmd/reconciler     reconciliation worker/CLI
internal/ledger    core domain: posting, balances, holds, reversal (no HTTP, no Kafka imports)
internal/idempotency
internal/outbox
internal/reconciliation
internal/httpapi   handlers, middleware, problem+json errors
internal/platform  db, config, telemetry
migrations/
docs/              schema.sql, openapi.yaml, ADRs (docs/adr/NNNN-title.md)
deploy/            docker-compose.yml, grafana dashboards

## Working agreement
- Work in small steps. After each step: build passes, tests pass, then summarize what changed.
- Write the failing test first for anything in internal/ledger.
- Prefer boring, explicit Go. No reflection tricks, no generic frameworks, no clever abstractions.
- When you make a design decision (isolation level, lock strategy, retry policy), write a
  short ADR in docs/adr/ stating the options considered and why this one was chosen.
- If a requirement is ambiguous, ask me instead of guessing.
- Keep comments for "why", not "what".
- Never swallow errors. Map domain errors to the problem+json codes defined in openapi.yaml.