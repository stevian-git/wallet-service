-- =============================================================================
-- 0003: reconciliation scope per settlement provider (ADR 0008)
-- =============================================================================
-- * settlement_providers: each provider's business-day cut-off (time zone and
--   local time), registered through the admin API.
-- * settlement_provider_accounts: each provider has its own system_bank account
--   per currency. Top-ups and withdrawals settled by a provider post against
--   that account, which is what scopes a run to the provider's transfers.
-- * reconciliation_runs records the scope a run used, for audit.
-- Declarative DDL and GRANTs only (no triggers/functions); runnable by ledger_migrator.

BEGIN;

CREATE TABLE settlement_providers (
  provider    text        PRIMARY KEY CHECK (provider ~ '^[a-z0-9_]{1,64}$'),
  cutoff_tz   text        NOT NULL,          -- IANA name, validated in Go (time.LoadLocation)
  cutoff_time time        NOT NULL,          -- end of the local business day; 00:00 = midnight at its end
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE settlement_provider_accounts (
  provider   text    NOT NULL REFERENCES settlement_providers (provider),
  currency   char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  account_id uuid    NOT NULL UNIQUE,
  PRIMARY KEY (provider, currency),
  -- Composite FK: the linked account must have the same currency.
  FOREIGN KEY (account_id, currency) REFERENCES accounts (id, currency)
);

-- NULL scope_provider with a scope = run used the legacy unscoped UTC day.
ALTER TABLE reconciliation_runs
  ADD COLUMN scope_start    timestamptz,
  ADD COLUMN scope_end      timestamptz,
  ADD COLUMN scope_provider text,
  ADD CONSTRAINT reconciliation_runs_scope_order CHECK (scope_end > scope_start);

GRANT SELECT, INSERT ON settlement_providers, settlement_provider_accounts TO ledger_app;
GRANT UPDATE (cutoff_tz, cutoff_time, updated_at)     ON settlement_providers TO ledger_app;
GRANT UPDATE (scope_start, scope_end, scope_provider) ON reconciliation_runs  TO ledger_app;

COMMIT;
