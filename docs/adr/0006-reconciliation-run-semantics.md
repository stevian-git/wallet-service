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
3. Server parses CSV and batch-inserts `settlement_records` (500 per batch via `COPY`)
4. Returns 201 with `SettlementFile` view

### Run Flow

1. Client creates run via `POST /v1/reconciliation/runs` with `file_id` and an
   `Idempotency-Key`; a retry with the same key replays the first run
2. Server inserts `reconciliation_runs` row with `status='running'`, returns 202
3. Worker polls for a `running` run that no other worker has locked
   (`FOR UPDATE SKIP LOCKED`; this pick holds no lock)
4. Worker executes reconciliation in a single `REPEATABLE READ` transaction whose
   first statement locks the run row (`FOR UPDATE SKIP LOCKED`) until commit.
   If the row is locked or the run is no longer `running`, the worker skips it:
   - Loads all `settlement_records` for the file
   - Loads transfers in scope (non-reversal, with reference, within settlement_date UTC)
   - Compares by reference, detects discrepancies
   - Batch-inserts discrepancies (500 per batch via `COPY`)
   - Updates run status to `completed` with counts (only while still `running`)
5. If worker crashes: the lock is released with its connection and the run
   remains `running`, so another worker retries it
6. If reconciliation fails: run marked `failed` with error message (only while
   still `running`, so a late failure cannot overwrite a completed run)

### Discrepancy Kinds

- `missing_in_ledger`: settlement record exists, no matching transfer
- `missing_in_settlement`: transfer exists, no matching settlement record
- `amount_mismatch`: both exist, amounts differ (expected=ledger, actual=settlement)
- `status_mismatch`: both exist, reversal status differs
- `duplicate_in_settlement`: multiple settlement records with same `our_reference`

### Scope

Transfers in scope for a reconciliation run:
- `created_at` within `[settlement_date 00:00 UTC, +1 day)`
- `reference IS NOT NULL`
- `type != 'reversal'`

Settlement records matched by `our_reference` (nullable). Records with
`our_reference IS NULL` are flagged as `missing_in_ledger`.

### Concurrency

- Multiple workers can run concurrently
- A run is executed by at most one worker at a time: the run row lock is held
  for the whole execution transaction, and other workers skip locked runs
- Advisory locks not used; row-level locking sufficient
- Multiple runs per file allowed (no one-run-per-file constraint)

### Streaming

- Settlement records loaded in full for the file (acceptable for typical file sizes)
- Transfers loaded via keyset pagination if needed (not yet implemented)
- Discrepancies inserted in batches of 500 via `pgx.CopyFrom`

## Consequences

### Positive

- Crash-safe: interrupted runs can be retried without data loss
- At most once per run: concurrent workers cannot both record discrepancies for
  one run; comparison output is deterministic (references are compared in sorted order)
- Scalable: batch inserts handle large files efficiently
- Flexible: multiple runs per file allow re-processing after corrections

### Negative

- Full settlement load: if files exceed memory, need to implement streaming comparison
- No lease or attempt counter yet: a run that crashes the worker (e.g. OOM on a
  very large file) stays `running` and is retried forever at the head of the
  queue. Needs a schema change (attempts / lease columns).
- The run row stays locked for the whole run, so very large runs hold a long
  `REPEATABLE READ` snapshot (delays vacuum).

### Neutral

- Discrepancies are append-only per run; resolution (open/resolved/ignored) is separate
- Reversal detection uses `EXISTS (SELECT 1 FROM transfers r WHERE r.reverses_transfer_id = t.id)`
  which adds a subquery per transfer; acceptable for typical volumes
