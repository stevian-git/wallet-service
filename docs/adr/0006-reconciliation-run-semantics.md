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

1. Client creates run via `POST /v1/reconciliation/runs` with `file_id`
2. Server inserts `reconciliation_runs` row with `status='running'`, returns 202
3. Worker polls for `running` runs, claims one with `FOR UPDATE SKIP LOCKED`
4. Worker executes reconciliation in a single `REPEATABLE READ` transaction:
   - Loads all `settlement_records` for the file
   - Loads transfers in scope (non-reversal, with reference, within settlement_date UTC)
   - Compares by reference, detects discrepancies
   - Batch-inserts discrepancies (500 per batch via `COPY`)
   - Updates run status to `completed` with counts
5. If worker crashes: run remains `running`, can be retried by another worker
6. If reconciliation fails: run marked `failed` with error message

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
- Each worker claims one run at a time with `FOR UPDATE SKIP LOCKED`
- Advisory locks not used; row-level locking sufficient
- Multiple runs per file allowed (no one-run-per-file constraint)

### Streaming

- Settlement records loaded in full for the file (acceptable for typical file sizes)
- Transfers loaded via keyset pagination if needed (not yet implemented)
- Discrepancies inserted in batches of 500 via `pgx.CopyFrom`

## Consequences

### Positive

- Crash-safe: interrupted runs can be retried without data loss
- Idempotent: same run executed twice produces same result (discrepancies deduplicated by run_id)
- Scalable: batch inserts handle large files efficiently
- Flexible: multiple runs per file allow re-processing after corrections

### Negative

- Full settlement load: if files exceed memory, need to implement streaming comparison
- No advisory locks: if a worker holds a run for too long, another worker cannot steal it
  (mitigated by worker timeout or manual intervention)

### Neutral

- Discrepancies are append-only per run; resolution (open/resolved/ignored) is separate
- Reversal detection uses `EXISTS (SELECT 1 FROM transfers r WHERE r.reverses_transfer_id = t.id)`
  which adds a subquery per transfer; acceptable for typical volumes
