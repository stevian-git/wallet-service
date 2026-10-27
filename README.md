# Mini Ledger & Wallet Service

Portfolio-grade wallet service built on a double-entry ledger. The project
focuses on correctness, explicit failure handling, and a small, reviewable
posting path.

## What is implemented

- Accounts: create, read, balance, and cursor-paginated ledger entries.
- Money movements: top-up, wallet-to-wallet transfer, withdrawal, and reversal.
- Holds: create, read, partial/full capture, release, and background expiry.
- One posting path (`internal/ledger.Post`) with balanced-entry validation.
- Deterministic balance-row locking and an `account_balances` cache updated in
  the same transaction as `ledger_entries`.
- Idempotency for every money-moving endpoint (accounts, top-ups, transfers,
  withdrawals, reversals, holds, capture, and release):
  - same key + same body = replayed response;
  - same key + different body = `422`;
  - in-flight duplicate = `409`.
- RFC 7807 `application/problem+json` errors.
- Transactional outbox rows for `account.created`, `transfer.posted`,
  `transfer.reversed`, `hold.created`, `hold.captured`, `hold.released`, and
  `hold.expired`.
- Outbox relay to Kafka (`cmd/relay`) with per-aggregate ordering, durable
  retry state, and `FOR UPDATE SKIP LOCKED` claiming for multiple instances.
- Demo Kafka consumer (`cmd/consumer-demo`) with `event_id` dedupe.
- Hold expiry worker (`cmd/holdsweeper`) safe to run as multiple instances.
- Reconciliation: import settlement CSV files, create reconciliation runs, and
  query discrepancies. Worker (`cmd/reconciler`) claims and processes runs
  with crash-safe retry.
- `GET /v1/admin/integrity` backed by the schema's read-only integrity views.
- Append-only enforcement through database privileges and a startup role check.
- Integration tests against real PostgreSQL and Redpanda via testcontainers.

Not yet implemented: metrics and distributed tracing.
The HTTP contract for holds and reconciliation is documented in
`docs/openapi.yaml`.

## Quick start

Requirements: Go 1.25+, Docker.

```bash
cp .env.example .env

make up          # PostgreSQL 16 on :5436 and Redpanda on :9092
make migrate     # run migrations as ledger_migrator
make kafka-topic # optional: create ledger.events.v1 with 3 partitions
make run         # start the API with the ledger_app role
```

The API listens on `:8080` by default. `make run` uses the development API key
`dev_api_key`.

Run the background processes in separate terminals:

```bash
make holdsweeper   # expire due holds every 5s
make relay         # publish outbox rows to Kafka
make consumer-demo # print and dedupe events
make reconciler    # process reconciliation runs
```

```bash
curl -s http://localhost:8080/healthz
curl -s http://localhost:8080/readyz

curl -s -X POST http://localhost:8080/v1/accounts \
  -H 'Content-Type: application/json' \
  -H 'X-API-Key: dev_api_key' \
  -H 'Idempotency-Key: create-account-1' \
  -d '{"owner_ref":"alice","currency":"IDR"}'
```

Keep the returned account ID and use it for a top-up:

```bash
curl -s -X POST http://localhost:8080/v1/topups \
  -H 'Content-Type: application/json' \
  -H 'X-API-Key: dev_api_key' \
  -H 'Idempotency-Key: topup-1' \
  -d '{"destination_account_id":"ACCOUNT_ID","amount":100000,"currency":"IDR"}'
```

Authorize part of the balance as a hold:

```bash
curl -s -X POST http://localhost:8080/v1/holds \
  -H 'Content-Type: application/json' \
  -H 'X-API-Key: dev_api_key' \
  -H 'Idempotency-Key: hold-1' \
  -d '{"account_id":"ACCOUNT_ID","amount":30000,"currency":"IDR","ttl_seconds":300,"reference":"order-1"}'
```

Capture less than the hold (`amount` omitted or `0` captures the full hold; the
remainder is released automatically):

```bash
curl -s -X POST http://localhost:8080/v1/holds/HOLD_ID/capture \
  -H 'Content-Type: application/json' \
  -H 'X-API-Key: dev_api_key' \
  -H 'Idempotency-Key: capture-1' \
  -d '{"destination_account_id":"DESTINATION_ACCOUNT_ID","amount":20000}'
```

Release it instead:

```bash
curl -s -X POST http://localhost:8080/v1/holds/HOLD_ID/release \
  -H 'X-API-Key: dev_api_key' \
  -H 'Idempotency-Key: release-1'
```

Retrying the same request with the same `Idempotency-Key` and body returns the
original response and sets `Idempotent-Replayed: true`.

## Reconciliation

Import a settlement CSV file (columns: `provider_ref,our_reference,amount,currency,status,occurred_at`):

```bash
curl -s -X POST http://localhost:8080/v1/reconciliation/settlement-files \
  -H 'X-API-Key: dev_api_key' \
  -F 'provider=mock_bank_a' \
  -F 'settlement_date=2026-10-26' \
  -F 'file=@settlement.csv'
```

Create a reconciliation run:

```bash
curl -s -X POST http://localhost:8080/v1/reconciliation/runs \
  -H 'Content-Type: application/json' \
  -H 'X-API-Key: dev_api_key' \
  -d '{"file_id":"FILE_ID"}'
```

The reconciler worker (`make reconciler`) polls for `running` runs, claims them,
and processes them. Query discrepancies:

```bash
curl -s http://localhost:8080/v1/reconciliation/runs/RUN_ID/discrepancies \
  -H 'X-API-Key: dev_api_key'
```

Generate a mock settlement CSV from existing transfers:

```bash
make mock-settlement MOCK_OUTPUT=settlement.csv MOCK_DATE=2026-10-26
```

## Tests

```bash
make test
```

Tests start disposable PostgreSQL 16 and Redpanda containers. Docker must be
running. The suite covers:

- the ledger happy path, reversal, double-reversal rejection, and append-only
  database privileges;
- insufficient funds leaving no partial ledger or outbox writes;
- concurrent transfers and holds without overdraft or balance-cache drift;
- hold partial capture, release, expiry, and expired-capture rejection;
- idempotency replay, key reuse, in-flight conflicts, and transient retries;
- the HTTP contract end to end, including holds, problem responses, and
  pagination;
- outbox relay ordering, mid-batch failure recovery, and two relays without
  duplicate publication;
- Kafka producer key/headers/payload and consumer dedupe TTL/cap behavior.

## Database roles

The API, relay, and sweeper never connect as the schema owner.

- `ledger_migrator`: owns the schema and runs migrations.
- `ledger_app`: runtime role with only `SELECT`/`INSERT` on append-only tables
  and narrowly scoped `UPDATE` grants. It cannot `UPDATE`, `DELETE`, or
  `TRUNCATE` `ledger_entries` or `audit_log`.

`db.VerifyAppRole` checks the current role at startup and refuses to run as a
superuser or as a role that can mutate append-only tables.

## Layout

```text
cmd/api              HTTP server wiring
cmd/migrate          migration CLI
cmd/holdsweeper      hold expiry worker
cmd/relay            outbox -> Kafka relay
cmd/consumer-demo    demo Kafka consumer with event_id dedupe
cmd/reconciler       reconciliation worker
cmd/mock-settlement  mock settlement CSV generator
internal/ledger      posting, balances, holds, reversal, integrity views
internal/idempotency idempotency reservation/replay service
internal/outbox      outbox claiming, relay, Kafka publisher
internal/consumer    demo consumer dedupe
internal/reconciliation settlement import, reconciliation logic, service
internal/app         application orchestration and response views
internal/httpapi     routing, middleware, problem+json responses
internal/problem     domain error -> RFC 7807 mapping
internal/platform    config, database, logging
migrations/          golang-migrate migrations (schema copied verbatim)
deploy/postgres/init role provisioning
docs/                schema, OpenAPI contract, ADRs
```

## Design notes

- Money is `int64` minor units. No floating point or decimal arithmetic.
- Corrections are new reversing transfers. Ledger entries are never updated or
  deleted.
- Balance changes and ledger entries share one transaction. Accounts are locked
  with `ORDER BY account_id ... FOR UPDATE`.
- Holds reduce available balance (`posted - held`) without posting. Capture
  posts exactly the captured amount and releases the remainder. See
  `docs/adr/0005-lock-ordering-vs-optimistic-locking.md`.
- The `account_balances` check constraint is only a backstop. SQLSTATE `23514`
  is logged as an error and mapped to `ErrInsufficientFunds`.
- Idempotency completion is written in the same transaction as the posting. See
  `docs/adr/0002-idempotency.md`.
- Outbox rows are written in the ledger transaction and published later by the
  relay. Delivery is at-least-once; consumers must dedupe by `event_id`. See
  `docs/adr/0004-outbox-vs-dual-write.md`.
- Settlement files are deduplicated by SHA-256 hash. Reconciliation runs compare
  settlement records against ledger transfers in scope (by date and reference).
  See `docs/adr/0006-reconciliation-run-semantics.md`.
- Database triggers and stored procedures are intentionally not used. See
  `docs/adr/0001-no-triggers-stored-procedures.md`.

### Kafka event contract

Topic: `ledger.events.v1` (default). Key: `aggregate_id`. Headers include
`event_id`, `schema_version`, and any trace headers supplied by future
instrumentation.

```text
account.created   { account_id, owner_ref, currency }
transfer.posted   { transfer_id, type, amount, currency, source_account_id, destination_account_id, reference }
transfer.reversed { transfer_id, reverses_transfer_id, amount, currency }
hold.created      { hold_id, account_id, amount, currency, expires_at }
hold.captured     { hold_id, transfer_id, amount }
hold.released     { hold_id }
hold.expired      { hold_id }
```

### Current deviations from the original stack notes

- Routing uses the standard library `net/http` `ServeMux` rather than chi. The
  route surface is small and the standard mux supports method/path patterns.
- SQL is currently explicit pgx rather than sqlc. The queries are centralized in
  `internal/ledger`; sqlc can be introduced without changing the domain API.
- Tests use the standard `testing` package rather than testify. Assertions are
  intentionally small and local to each test.
