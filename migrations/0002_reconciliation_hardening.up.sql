-- =============================================================================
-- 0002: reconciliation hardening
-- =============================================================================
-- * Retry with backoff and an attempt limit, so a run that keeps crashing the
--   worker cannot block the queue forever.
-- * At most one active run per settlement file.
-- * A corrected settlement file supersedes the previous one for the same
--   provider and settlement date.
-- * Index for the reconciler's transfer scope query.
-- * Two new discrepancy kinds.
-- Declarative DDL and GRANTs only (no triggers/functions); runnable by ledger_migrator.
-- The new enum values are added inside the transaction but not used in it,
-- which PostgreSQL 12+ allows.

BEGIN;

ALTER TYPE discrepancy_kind ADD VALUE IF NOT EXISTS 'duplicate_in_ledger';
ALTER TYPE discrepancy_kind ADD VALUE IF NOT EXISTS 'currency_mismatch';

-- -----------------------------------------------------------------------------
-- Reconciliation runs: attempts + backoff, one active run per file
-- -----------------------------------------------------------------------------
ALTER TABLE reconciliation_runs
  ADD COLUMN attempts        int         NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  ADD COLUMN next_attempt_at timestamptz NOT NULL DEFAULT now();

-- Existing data: keep only the newest running run per file active.
UPDATE reconciliation_runs r
   SET status = 'failed',
       finished_at = now(),
       error = 'superseded by newer run (migration 0002)'
 WHERE r.status = 'running'
   AND EXISTS (
         SELECT 1 FROM reconciliation_runs n
          WHERE n.file_id = r.file_id
            AND n.status = 'running'
            AND (n.started_at, n.id) > (r.started_at, r.id));

CREATE UNIQUE INDEX recon_runs_one_active_per_file
  ON reconciliation_runs (file_id) WHERE status = 'running';

CREATE INDEX recon_runs_claim_idx
  ON reconciliation_runs (next_attempt_at) WHERE status = 'running';

-- -----------------------------------------------------------------------------
-- Settlement files: a corrected file supersedes the previous one
-- -----------------------------------------------------------------------------
-- DEFERRABLE so an import can point the old file at the new one before the new
-- row is inserted, in the same transaction.
ALTER TABLE settlement_files
  ADD COLUMN superseded_by uuid REFERENCES settlement_files (id) DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT settlement_files_not_self_superseded CHECK (superseded_by <> id);

-- Existing data: the most recently imported file per provider/date stays active.
UPDATE settlement_files f
   SET superseded_by = latest.id
  FROM (SELECT DISTINCT ON (provider, settlement_date) id, provider, settlement_date
          FROM settlement_files
         ORDER BY provider, settlement_date, imported_at DESC, id DESC) latest
 WHERE f.provider = latest.provider
   AND f.settlement_date = latest.settlement_date
   AND f.id <> latest.id;

CREATE UNIQUE INDEX settlement_files_one_active
  ON settlement_files (provider, settlement_date) WHERE superseded_by IS NULL;

-- -----------------------------------------------------------------------------
-- Reconciler scope query: transfers created on the settlement date
-- -----------------------------------------------------------------------------
CREATE INDEX transfers_created_at_ref_idx
  ON transfers (created_at) WHERE reference IS NOT NULL;

-- -----------------------------------------------------------------------------
-- Grants for the application role
-- -----------------------------------------------------------------------------
GRANT UPDATE (attempts, next_attempt_at) ON reconciliation_runs TO ledger_app;
GRANT UPDATE (superseded_by)             ON settlement_files    TO ledger_app;

COMMIT;
