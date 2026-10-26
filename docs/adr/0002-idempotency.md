# ADR-0002: Idempotency with a short reservation lease and atomic completion

- Status: Accepted
- Date: 2026-10-26

## Context

Every money-moving endpoint must accept an `Idempotency-Key` header and guarantee:

- the same key with the same request body returns the original response;
- the same key with a different request body returns `422`;
- a duplicate request while the first is still running returns `409`;
- the idempotency result and the ledger write commit atomically.

The ledger write can take multiple statements: insert the transfer, insert
ledger entries, update `account_balances`, and insert the outbox event. Posting
the same key twice must never create two transfers.

## Options considered

### A. Store the idempotency key on `transfers`

Add a unique `(scope, key)` column to `transfers` and let the ledger transaction
insert it. A duplicate key would raise a unique violation, which the service
could use to fetch the original transfer.

Advantages:
- One transaction and one row per operation.
- No lease or cleanup process.

Disadvantages:
- `docs/schema.sql` would need a new column and unique index. The schema is the
  source of truth and this change is not required by the current contract.
- It only covers operations that create a transfer. It does not provide a
  generic mechanism for responses, error responses, or future non-transfer
  money endpoints.
- Distinguishing a request that is still running from one that already completed
  requires extra state.

### B. Reserve the key in a separate short transaction, then complete it inside the ledger transaction (chosen)

Use the existing `idempotency_keys` table:

1. Insert `(scope, key, request_hash, status='in_progress', locked_until=lease)`
   in its own transaction. A unique conflict means another request owns the key.
2. If a row exists, lock it with `SELECT ... FOR UPDATE`:
   - completed and the same hash: replay the stored response;
   - completed or in progress with a different hash: `422`;
   - in progress with a live lease: `409`;
   - in progress with an expired lease: take over the lease.
3. Run the posting in one database transaction. On success, update the
   idempotency row to `completed` with the response code/body in that same
   transaction.

Advantages:
- Reuses the schema that already exists.
- The completion and the ledger write are atomic. If the ledger transaction
  rolls back, the key remains `in_progress` and can be retried after the lease.
- A process crash leaves an explicit lease instead of a permanently stuck key.
- Stable domain errors (`4xx`) are cached and replayed too. Transient failures
  (`5xx`) release the key so the client can retry.

Disadvantages:
- The initial reservation is a separate transaction, so it is not atomic with
  the posting. This is safe because the completion update is atomic with the
  posting, and the worst case is a short-lived `409` while the first request is
  running.
- Requires a lease duration to be chosen. The default is 30 seconds and is
  configurable with `IDEMPOTENCY_LEASE_SECONDS`.
- Requires a cleanup policy for completed keys. The table has `expires_at`
  (24 hours by default); expired rows are replaced lazily when the same key is
  used again. A periodic cleanup job can be added later without changing the
  request path.

### C. PostgreSQL advisory locks keyed by hash

Advantages:
- No persistent reservation row.

Disadvantages:
- Advisory locks are session-scoped and interact badly with connection pools.
- The first response still has to be stored somewhere for replay.
- Crash recovery is tied to connection lifetime rather than an explicit lease.

## Decision

Use option B.

- Scope is derived from the API key (`sha256(API key)[:16]`), so two tenants
  cannot collide on a key, and the raw secret is not stored.
- The request hash is `sha256(method + path + body)`.
- Same key and same hash while `in_progress` with a live lease returns `409`.
- Same key and a different hash returns `422 idempotency_key_reused`.
- Completed responses are stored as JSONB. PostgreSQL may normalize whitespace,
  so the stored form is also the form returned on the first request.
- Stable `4xx` responses are cached; `5xx` responses release the key.
- The lease is 30 seconds by default. A crashed request can be retried after the
  lease expires.

## Consequences

- The API never executes the posting twice for a completed key.
- A retry that arrives while the first request is still running receives `409`
  and may retry later.
- The response body of a money endpoint is stored in the database for 24 hours.
  Do not return secrets or large payloads from these endpoints.
- The idempotency table is not the ledger. It may be deleted or expired; the
  ledger and outbox remain the source of truth.
