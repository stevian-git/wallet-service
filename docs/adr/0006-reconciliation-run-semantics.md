# ADR 0006: Reconciliation Run Semantics

## Status

Accepted.

## Context

The ledger needs to reconcile its transfers against external settlement files
provided by payment providers or banks. A reconciliation run compares settlement
records against ledger transfers within a specific date range, producing
discrepancies that operators can review and resolve.

Key requirements:
- Support multiple runs per settlement file (e.g., re-run after corrections)
- Crash-safe retry: if a worker crashes mid-run, the run can be retried
- Streaming comparison: support 1M+ rows without loading everything into memory
- Deterministic discrepancy detection: same inputs produce same discrepancies

## Decision

### Data Model

- `settlement_files`: metadata about imported CSV files (provider, date, SHA-256)
- `settlement_records`: individual rows from the CSV, linked by `file_id`
- `reconciliation_runs`: tracks each reconciliation attempt (status, counts, errors)
- `reconciliation_discrepancies`: individual discrepancies found during a run

### Import Flow

1. Client uploads CSV via `POST /v1/reconciliation/settlement-files` (multipart)
2. Server computes SHA-256 and checks for duplicates (409 if exists)
3. In one transaction, the server:
   - takes `pg_advisory_xact_lock` on (provider, settlement_date), so imports for
     the same provider and date are serialized and others are not blocked
   - marks the active file for that provider and date as `superseded_by` the new
     file (the FK is `DEFERRABLE INITIALLY DEFERRED`, so this runs before the INSERT)
   - inserts the file row and streams `settlement_records` with one `COPY`
4. Returns 201 with `SettlementFile` view

A partial unique index (`provider, settlement_date WHERE superseded_by IS NULL`)
is the backstop: there is never more than one active file per provider and date.
A superseded file cannot get a new run (409 `file_superseded`); runs already
created for it may still complete.

### Run Flow

1. Client creates run via `POST /v1/reconciliation/runs` with `file_id` and an
   `Idempotency-Key`; a retry with the same key replays the first run
2. Server inserts `reconciliation_runs` row with `status='running'`, returns 202.
   A partial unique index allows only one `running` run per file; a second
   request with a different key gets 409 `run_in_progress`
3. Worker claims the `running` run with the earliest `next_attempt_at <= now()`
   that no other worker has locked (`FOR UPDATE SKIP LOCKED`). The claim is one
   committed `UPDATE` that increments `attempts` and pushes `next_attempt_at`
   out by an exponential backoff (`RECONCILER_RETRY_BASE` doubled per attempt,
   capped at `RECONCILER_RETRY_MAX`). A run past `RECONCILER_MAX_ATTEMPTS` is
   marked `failed` instead of executed. The claim itself holds no lock
4. Worker executes reconciliation in a single `REPEATABLE READ` transaction whose
   first statement locks the run row (`FOR UPDATE SKIP LOCKED`) until commit.
   If the row is locked or the run is no longer `running`, the worker skips it:
   - Loads all `settlement_records` for the file
   - Loads transfers in scope (non-reversal, with reference, within the provider's
     business day and posted against its bank account; see Scope and ADR 0008)
   - Compares by reference, detects discrepancies
   - Batch-inserts discrepancies (500 per batch via `COPY`)
   - Updates run status to `completed` with counts (only while still `running`)
5. If worker crashes: the lock is released with its connection and the run
   remains `running`. It is retried once its backoff expires, and it does not
   block other runs in the meantime
6. If reconciliation fails: run marked `failed` with error message (only while
   still `running`, so a late failure cannot overwrite a completed run)

### Discrepancy Kinds

- `missing_in_ledger`: settlement record exists, no matching transfer
- `missing_in_settlement`: transfer exists, no matching settlement record
- `amount_mismatch`: both exist, amounts differ (expected=ledger, actual=settlement)
- `status_mismatch`: both exist, reversal status differs
- `duplicate_in_settlement`: multiple settlement records with same `our_reference`
- `duplicate_in_ledger`: multiple ledger transfers with the same `reference`
- `currency_mismatch`: both exist, currencies differ (amounts are not compared)

Settlement records with status `failed` do not count as paid: if every record
for a reference failed but the ledger posted the transfer, it is a
`status_mismatch`.

### Scope

Transfers in scope for a reconciliation run:
- `reference IS NOT NULL`
- `type != 'reversal'`
- `status = 'posted'`
- `created_at` within the file provider's business day, and the transfer posts
  against one of the provider's bank accounts (ADR 0008)
- If the file's provider is not registered: every transfer that matches the
  three conditions above, with `created_at` within
  `[settlement_date 00:00 UTC, +1 day)` (the scope before ADR 0008)

Each completed run records the scope it used in `scope_start`, `scope_end`, and
`scope_provider`.

Settlement records matched by `our_reference` (nullable). Records with
`our_reference IS NULL` are flagged as `missing_in_ledger`.

### Concurrency

- Multiple workers can run concurrently
- A run is executed by at most one worker at a time: the run row lock is held
  for the whole execution transaction, and other workers skip locked runs
- Run execution uses row locks only. Imports use a transaction-scoped advisory
  lock per (provider, settlement_date); without it, N concurrent imports would
  need N retries on the one-active-file index
- At most one active run per file; a file can be reconciled again once its
  previous run finished

### Streaming

Memory is bounded independently of file size. Measured on a 1M-row (83 MB)
file:

| | Peak RSS before | Peak RSS after | Time before | Time after |
|---|---|---|---|---|
| Import (API) | 713 MiB | 31 MiB | ~7s | ~6s |
| Run (reconciler) | 1099 MiB | 27 MiB | ~11s | ~14.5s |

The run is ~3s slower. Postgres now sorts the file's records by
`our_reference COLLATE "C"` instead of reading them in primary-key order. The
cursor fetch size does not change this: 1000 and 5000 measure the same. An
index on `settlement_records (file_id, our_reference COLLATE "C", id)` would
remove the sort, at the cost of a slower import and a migration. It has not
been added.

- **Import** reads the upload twice and never buffers it. The multipart part is
  spooled to a temp file (1 MiB form memory).
  - The first pass validates every row, counts them, and hashes the bytes. The
    `settlement_files` row (sha256, record_count) must exist before its
    records, and a bad row is reported (422) before the database is touched.
  - The second pass rewinds and streams rows into `COPY` via a
    `pgx.CopyFromSource`.
- **Execute** declares two server-side cursors in the run's `REPEATABLE READ`
  transaction, `FETCH`es them 1000 rows at a time, and merge-joins them by
  reference:
  - `settlement_records ORDER BY our_reference COLLATE "C" NULLS LAST, id`
  - in-scope `transfers ORDER BY reference COLLATE "C", created_at, id`
- **Why `COLLATE "C"`:** it is byte order, the same as Go's string comparison.
  The database default collation (glibc or ICU `en_US`) orders `b`/`B`/`ä`
  differently, and a merge-join over a different order silently mismatches.
  The in-memory `compare` used by tests runs the same merge over sorted
  slices, and a test asserts that streamed and in-memory results are
  identical.
- **Memory** is one cursor batch per side, one reference group, and a
  5000-row discrepancy batch, written with `COPY` as it fills.
- **The trade-off kept:** one transaction per run, so the run sees one
  consistent snapshot and is all-or-nothing. A long run therefore holds a
  snapshot (delays vacuum). `RECONCILER_RUN_TIMEOUT` (default 30m) bounds it;
  a run that exceeds it is marked `failed` instead of retried, since a retry
  would time out again.

## Consequences

### Positive

- Crash-safe: interrupted runs can be retried without data loss
- At most once per run: concurrent workers cannot both record discrepancies for
  one run; comparison output is deterministic (references are compared in sorted order)
- Scalable: batch inserts handle large files efficiently
- Flexible: a file can be re-run after its previous run finished, and a
  corrected file supersedes the old one instead of being reconciled beside it
- Bounded retries: a poison run fails after `RECONCILER_MAX_ATTEMPTS` claims and
  waits out its backoff between attempts instead of blocking the queue

### Negative

- An attempt is counted at claim time, so a worker that claims a run and then
  loses the race to execute it (rare) uses up one attempt
- A run whose worker died waits for its backoff (default 30s) before retrying
- The run row stays locked for the whole run, so very large runs hold a long
  `REPEATABLE READ` snapshot (delays vacuum).

### Neutral

- Discrepancies are append-only per run; resolution (open/resolved/ignored) is separate
- Reversal detection uses `EXISTS (SELECT 1 FROM transfers r WHERE r.reverses_transfer_id = t.id)`,
  an index lookup per transfer on the unique `reverses_transfer_id`
- Ordering by `COLLATE "C"` cannot use the default-collation index on
  `transfers.reference`; Postgres sorts the day's scope (spilling to disk past
  `work_mem`), which keeps the cost in the database instead of in worker memory
