# Mini Ledger & Wallet Service

Portfolio-grade wallet service built on a double-entry ledger. The project
focuses on correctness, explicit failure handling, and a small, reviewable
posting path.

## What is implemented

- Accounts: create, read, balance, and cursor-paginated ledger entries.
- Money movements: top-up, wallet-to-wallet transfer, withdrawal, and reversal.
- One posting path (`internal/ledger.Post`) with balanced-entry validation.
- Deterministic balance-row locking and an `account_balances` cache updated in
  the same transaction as `ledger_entries`.
- Idempotency for every money-moving endpoint (accounts, top-ups, transfers,
  withdrawals, reversals):
  - same key + same body = replayed response;
  - same key + different body = `422`;
  - in-flight duplicate = `409`.
- RFC 7807 `application/problem+json` errors.
- Transactional outbox rows for `account.created`, `transfer.posted`, and
  `transfer.reversed`.
- `GET /v1/admin/integrity` backed by the schema's read-only integrity views.
- Append-only enforcement through database privileges and a startup role check.
- Integration tests against real PostgreSQL via testcontainers.

Not yet implemented: holds, reconciliation/import, Kafka relay/consumer,
metrics, and distributed tracing. The HTTP contract for holds and
reconciliation is documented in `docs/openapi.yaml`.

## Quick start

Requirements: Go 1.25+, Docker.

```bash
cp .env.example .env

make up          # PostgreSQL 16 on :5436
make migrate     # run migrations as ledger_migrator
make run         # start the API with the ledger_app role
```

The API listens on `:8080` by default. `make run` uses the development API key
`dev_api_key`.

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

Retrying the same request with the same `Idempotency-Key` and body returns the
original response and sets `Idempotent-Replayed: true`.

## Tests

```bash
make test
```

Tests start a disposable PostgreSQL 16 container. Docker must be running. The
suite covers:

- the ledger happy path, reversal, and double-reversal rejection;
- append-only database privileges;
- insufficient funds leaving no partial ledger or outbox writes;
- concurrent transfers without overdraft or balance-cache drift;
- idempotency replay, key reuse, in-flight conflicts, and transient retries;
- the HTTP contract end to end, including problem responses and pagination.

## Database roles

The API never connects as the schema owner.

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
internal/ledger      posting, balances, reversal, integrity views
internal/idempotency idempotency reservation/replay service
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
- The `account_balances` check constraint is only a backstop. SQLSTATE `23514`
  is logged as an error and mapped to `ErrInsufficientFunds`.
- Idempotency completion is written in the same transaction as the posting. See
  `docs/adr/0002-idempotency.md`.
- Database triggers and stored procedures are intentionally not used. See
  `docs/adr/0001-no-triggers-stored-procedures.md`.

### Current deviations from the original stack notes

- Routing uses the standard library `net/http` `ServeMux` rather than chi. The
  route surface is small and the standard mux supports method/path patterns.
- SQL is currently explicit pgx rather than sqlc. The queries are centralized in
  `internal/ledger`; sqlc can be introduced without changing the domain API.
- Tests use the standard `testing` package rather than testify. Assertions are
  intentionally small and local to each test.
