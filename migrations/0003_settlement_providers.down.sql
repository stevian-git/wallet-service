-- Reverts 0003. Provider system_bank accounts stay in accounts (the ledger is
-- append-only and they may have entries); only the provider tables and the run
-- scope columns are dropped.

BEGIN;

REVOKE UPDATE (scope_start, scope_end, scope_provider) ON reconciliation_runs FROM ledger_app;

ALTER TABLE reconciliation_runs
  DROP CONSTRAINT IF EXISTS reconciliation_runs_scope_order,
  DROP COLUMN IF EXISTS scope_provider,
  DROP COLUMN IF EXISTS scope_end,
  DROP COLUMN IF EXISTS scope_start;

DROP TABLE IF EXISTS settlement_provider_accounts;
DROP TABLE IF EXISTS settlement_providers;

COMMIT;
