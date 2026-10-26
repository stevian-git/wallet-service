BEGIN;

DROP VIEW IF EXISTS v_balance_drift, v_unbalanced_transfers, v_posted_transfers_without_entries;

DROP TABLE IF EXISTS
  reconciliation_discrepancies,
  reconciliation_runs,
  settlement_records,
  settlement_files,
  audit_log,
  outbox_events,
  idempotency_keys,
  holds,
  ledger_entries,
  transfers,
  account_balances,
  accounts
CASCADE;

DROP TYPE IF EXISTS
  discrepancy_kind,
  recon_status,
  hold_status,
  transfer_status,
  transfer_type,
  entry_direction,
  normal_side,
  account_kind
CASCADE;

COMMIT;
