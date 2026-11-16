-- Reverts 0002. Data written under 0002 is not restored: superseded links,
-- attempt counters, and the runs failed by the 0002 backfill stay as they are.
-- PostgreSQL cannot drop enum values, so discrepancy_kind is rebuilt; this
-- intentionally FAILS if any discrepancy still uses 'duplicate_in_ledger' or
-- 'currency_mismatch' (resolve or migrate those rows first).

BEGIN;

REVOKE UPDATE (superseded_by)             ON settlement_files    FROM ledger_app;
REVOKE UPDATE (attempts, next_attempt_at) ON reconciliation_runs FROM ledger_app;

DROP INDEX IF EXISTS transfers_created_at_ref_idx;

DROP INDEX IF EXISTS settlement_files_one_active;
ALTER TABLE settlement_files
  DROP CONSTRAINT IF EXISTS settlement_files_not_self_superseded,
  DROP COLUMN IF EXISTS superseded_by;

DROP INDEX IF EXISTS recon_runs_claim_idx;
DROP INDEX IF EXISTS recon_runs_one_active_per_file;
ALTER TABLE reconciliation_runs
  DROP COLUMN IF EXISTS next_attempt_at,
  DROP COLUMN IF EXISTS attempts;

ALTER TYPE discrepancy_kind RENAME TO discrepancy_kind_0002;
CREATE TYPE discrepancy_kind AS ENUM (
  'missing_in_ledger',
  'missing_in_settlement',
  'amount_mismatch',
  'status_mismatch',
  'duplicate_in_settlement'
);
ALTER TABLE reconciliation_discrepancies
  ALTER COLUMN kind TYPE discrepancy_kind USING kind::text::discrepancy_kind;
DROP TYPE discrepancy_kind_0002;

COMMIT;
