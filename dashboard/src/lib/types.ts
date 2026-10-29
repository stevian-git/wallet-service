export interface Account {
  id: string;
  kind: 'user_wallet' | 'merchant' | 'system_bank' | 'system_fee' | 'system_suspense';
  owner_ref: string;
  currency: string;
  status: 'active' | 'frozen' | 'closed';
  created_at: string;
}

export interface Balance {
  account_id: string;
  currency: string;
  posted: number;
  held: number;
  available: number;
  as_of: string;
}

export interface LedgerEntry {
  id: number;
  transfer_id: string;
  account_id: string;
  direction: 'debit' | 'credit';
  amount: number;
  currency: string;
  balance_after: number;
  created_at: string;
}

export interface Transfer {
  id: string;
  type: 'topup' | 'transfer' | 'withdrawal' | 'hold_capture' | 'reversal';
  status: 'pending' | 'posted' | 'failed';
  amount: number;
  currency: string;
  source_account_id: string | null;
  destination_account_id: string | null;
  reference: string | null;
  reverses_transfer_id: string | null;
  metadata: Record<string, unknown>;
  created_at: string;
  posted_at: string | null;
}

export interface TransferDetail extends Transfer {
  entries: LedgerEntry[];
}

export interface SettlementFile {
  id: string;
  provider: string;
  file_name: string;
  settlement_date: string;
  record_count: number;
  imported_at: string;
}

export interface ReconciliationRun {
  id: string;
  file_id: string;
  status: 'running' | 'completed' | 'failed';
  started_at: string;
  finished_at: string | null;
  matched_count: number;
  discrepancy_count: number;
  error: string | null;
}

export interface Discrepancy {
  id: number;
  run_id: string;
  kind: 'missing_in_ledger' | 'missing_in_settlement' | 'amount_mismatch' | 'status_mismatch' | 'duplicate_in_settlement';
  transfer_id: string | null;
  settlement_record_id: number | null;
  expected_amount: number | null;
  actual_amount: number | null;
  resolution: 'open' | 'resolved' | 'ignored';
  note: string | null;
  created_at: string;
}

export interface IntegrityReport {
  ok: boolean;
  checked_at: string;
  balance_drift: Array<{
    account_id: string;
    cached_posted: number;
    derived_posted: number;
  }>;
  unbalanced_transfers: Array<{
    transfer_id: string;
    total_debit: number;
    total_credit: number;
  }>;
  posted_without_entries: string[];
}

export interface PaginatedResponse<T> {
  data: T[];
  next_cursor: string | null;
}

export interface ProblemDetail {
  type?: string;
  title: string;
  status: number;
  detail?: string;
  code?: string;
  request_id?: string;
}
