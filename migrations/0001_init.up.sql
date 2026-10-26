-- =============================================================================
-- Mini Core Banking / Wallet Ledger : initial schema (PostgreSQL 15+)
-- =============================================================================
-- Konvensi:
--   * Semua uang disimpan sebagai BIGINT dalam minor unit (IDR = exponent 0,
--     jadi 15000 berarti Rp15.000). Tidak pernah float/numeric untuk saldo.
--   * Skema ini sengaja TIDAK memakai trigger atau function. Isinya hanya constraint
--     deklaratif (PK/FK/UNIQUE/CHECK), view read-only, dan GRANT. Semua logika bisnis
--     ada di aplikasi, dan migration bisa dijalankan role non-superuser biasa.
--   * ledger_entries bersifat append-only. Koreksi = transfer baru (reversal).
--     Dijaga lewat hak akses (bagian GRANT di akhir file): role aplikasi hanya punya
--     SELECT dan INSERT pada tabel tersebut.
--   * Setiap transfer harus seimbang (total debit = total kredit). Dijaga di aplikasi
--     (satu fungsi Post() yang menolak entri tidak seimbang sebelum menyentuh DB),
--     lalu diverifikasi oleh view v_unbalanced_transfers, endpoint /v1/admin/integrity,
--     dan test.
--   * Saldo di account_balances adalah cache yang diturunkan dari ledger_entries dan
--     selalu diubah dalam transaksi yang sama dengan entri ledger.
--   * Tanda saldo mengikuti normal_side akun:
--       - wallet pengguna = liability, normal_side = 'credit'
--         (kredit menambah saldo, debit mengurangi)
--       - akun bank/settlement = asset, normal_side = 'debit'
--         (debit menambah saldo, kredit mengurangi)
-- =============================================================================

BEGIN;

CREATE TYPE account_kind     AS ENUM ('user_wallet', 'merchant', 'system_bank', 'system_fee', 'system_suspense');
CREATE TYPE normal_side      AS ENUM ('debit', 'credit');
CREATE TYPE entry_direction  AS ENUM ('debit', 'credit');
CREATE TYPE transfer_type    AS ENUM ('topup', 'transfer', 'withdrawal', 'hold_capture', 'reversal');
CREATE TYPE transfer_status  AS ENUM ('pending', 'posted', 'failed');
CREATE TYPE hold_status      AS ENUM ('active', 'captured', 'released', 'expired');
CREATE TYPE recon_status     AS ENUM ('running', 'completed', 'failed');
CREATE TYPE discrepancy_kind AS ENUM (
  'missing_in_ledger',        -- ada di file settlement, tidak ada di ledger kita
  'missing_in_settlement',    -- ada di ledger kita, tidak ada di file settlement
  'amount_mismatch',
  'status_mismatch',
  'duplicate_in_settlement'
);

-- -----------------------------------------------------------------------------
-- Accounts & balances
-- -----------------------------------------------------------------------------
CREATE TABLE accounts (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  kind           account_kind NOT NULL,
  owner_ref      text,                               -- id pengguna/merchant dari sistem lain
  currency       char(3)      NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  normal_side    normal_side  NOT NULL,
  allow_negative boolean      NOT NULL DEFAULT false, -- true hanya untuk akun sistem (mis. bank)
  status         text         NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'frozen', 'closed')),
  created_at     timestamptz  NOT NULL DEFAULT now(),
  UNIQUE (id, currency),                             -- target FK komposit dari ledger_entries
  UNIQUE (id, allow_negative)                        -- target FK komposit dari account_balances
);

-- Satu wallet/merchant per owner per mata uang
CREATE UNIQUE INDEX accounts_owner_currency_uniq
  ON accounts (owner_ref, currency)
  WHERE owner_ref IS NOT NULL AND kind IN ('user_wallet', 'merchant');

CREATE TABLE account_balances (
  account_id     uuid        PRIMARY KEY,
  allow_negative boolean     NOT NULL,                -- salinan accounts.allow_negative (dikunci FK di bawah)
  posted         bigint      NOT NULL DEFAULT 0,      -- saldo sesuai normal_side
  held           bigint      NOT NULL DEFAULT 0 CHECK (held >= 0), -- dana yang ditahan (holds aktif)
  version        bigint      NOT NULL DEFAULT 0,      -- naik setiap update (untuk debug / optimistic locking)
  updated_at     timestamptz NOT NULL DEFAULT now(),
  -- FK komposit memastikan salinan allow_negative tidak pernah berbeda dari tabel accounts
  FOREIGN KEY (account_id, allow_negative) REFERENCES accounts (id, allow_negative),
  -- Pengaman terakhir (backstop): saldo tersedia tidak boleh negatif untuk akun biasa.
  -- Aplikasi tetap WAJIB mengecek lebih dulu dan mengembalikan ErrInsufficientFunds;
  -- jika constraint ini sampai terpicu (SQLSTATE 23514), anggap itu bug atau race yang lolos.
  CHECK (allow_negative OR posted - held >= 0)
);
-- Saldo tersedia = posted - held

-- -----------------------------------------------------------------------------
-- Transfers (header) & ledger entries (detail)
-- -----------------------------------------------------------------------------
CREATE TABLE transfers (
  id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  type                   transfer_type   NOT NULL,
  status                 transfer_status NOT NULL DEFAULT 'posted',
  amount                 bigint          NOT NULL CHECK (amount > 0),
  currency               char(3)         NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  source_account_id      uuid REFERENCES accounts (id),
  destination_account_id uuid REFERENCES accounts (id),
  reference              text,                        -- referensi eksternal (mis. id dari PSP)
  reverses_transfer_id   uuid UNIQUE REFERENCES transfers (id), -- satu transfer hanya bisa di-reverse sekali
  metadata               jsonb           NOT NULL DEFAULT '{}'::jsonb,
  created_at             timestamptz     NOT NULL DEFAULT now(),
  posted_at              timestamptz,
  CHECK (source_account_id IS DISTINCT FROM destination_account_id)
);

CREATE INDEX transfers_source_idx      ON transfers (source_account_id, created_at DESC);
CREATE INDEX transfers_destination_idx ON transfers (destination_account_id, created_at DESC);
CREATE INDEX transfers_reference_idx   ON transfers (reference) WHERE reference IS NOT NULL;

CREATE TABLE ledger_entries (
  id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  transfer_id   uuid            NOT NULL REFERENCES transfers (id),
  account_id    uuid            NOT NULL,
  currency      char(3)         NOT NULL,
  direction     entry_direction NOT NULL,
  amount        bigint          NOT NULL CHECK (amount > 0),
  balance_after bigint          NOT NULL,             -- snapshot saldo posted setelah entri (untuk mutasi/statement)
  created_at    timestamptz     NOT NULL DEFAULT now(),
  -- FK komposit: mata uang entri harus sama dengan mata uang akun
  FOREIGN KEY (account_id, currency) REFERENCES accounts (id, currency)
);

CREATE INDEX ledger_entries_account_idx  ON ledger_entries (account_id, id DESC);
CREATE INDEX ledger_entries_transfer_idx ON ledger_entries (transfer_id);

-- -----------------------------------------------------------------------------
-- Holds (authorize -> capture / release)
-- -----------------------------------------------------------------------------
CREATE TABLE holds (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id   uuid        NOT NULL REFERENCES accounts (id),
  amount       bigint      NOT NULL CHECK (amount > 0),
  currency     char(3)     NOT NULL,
  status       hold_status NOT NULL DEFAULT 'active',
  reference    text,
  expires_at   timestamptz NOT NULL,
  transfer_id  uuid REFERENCES transfers (id),        -- terisi saat di-capture
  created_at   timestamptz NOT NULL DEFAULT now(),
  resolved_at  timestamptz,
  CHECK ((status = 'captured') = (transfer_id IS NOT NULL))
);

CREATE INDEX holds_active_expiry_idx ON holds (expires_at) WHERE status = 'active';
CREATE INDEX holds_account_idx       ON holds (account_id, status);

-- -----------------------------------------------------------------------------
-- Idempotency
-- -----------------------------------------------------------------------------
CREATE TABLE idempotency_keys (
  scope         text        NOT NULL,                 -- mis. client id / api key id
  key           text        NOT NULL,
  request_hash  bytea       NOT NULL,                 -- sha256 dari method+path+body; beda hash = 422
  status        text        NOT NULL CHECK (status IN ('in_progress', 'completed')),
  response_code int,
  response_body jsonb,
  locked_until  timestamptz,                          -- lease untuk request in_progress yang crash
  created_at    timestamptz NOT NULL DEFAULT now(),
  expires_at    timestamptz NOT NULL DEFAULT now() + interval '24 hours',
  PRIMARY KEY (scope, key)
);

CREATE INDEX idempotency_keys_expiry_idx ON idempotency_keys (expires_at);

-- -----------------------------------------------------------------------------
-- Transactional outbox
-- -----------------------------------------------------------------------------
CREATE TABLE outbox_events (
  id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, -- urutan global
  aggregate_type text        NOT NULL,                -- 'transfer', 'hold', ...
  aggregate_id   uuid        NOT NULL,                -- dipakai sebagai partition key Kafka
  event_type     text        NOT NULL,                -- 'transfer.posted', 'hold.captured', ...
  payload        jsonb       NOT NULL,
  headers        jsonb       NOT NULL DEFAULT '{}'::jsonb, -- trace context, schema version
  created_at     timestamptz NOT NULL DEFAULT now(),
  published_at   timestamptz,
  attempts       int         NOT NULL DEFAULT 0,
  last_error     text
);

-- Relay hanya membaca event yang belum terkirim
CREATE INDEX outbox_unpublished_idx ON outbox_events (id) WHERE published_at IS NULL;

-- -----------------------------------------------------------------------------
-- Reconciliation
-- -----------------------------------------------------------------------------
CREATE TABLE settlement_files (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  provider        text        NOT NULL,               -- 'mock_bank_a', 'mock_ewallet_b'
  file_name       text        NOT NULL,
  file_sha256     bytea       NOT NULL UNIQUE,        -- cegah impor file yang sama dua kali
  settlement_date date        NOT NULL,
  record_count    int         NOT NULL,
  imported_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE settlement_records (
  id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  file_id       uuid        NOT NULL REFERENCES settlement_files (id),
  provider_ref  text        NOT NULL,                 -- id transaksi di sisi provider
  our_reference text,                                 -- cocok dengan transfers.reference
  amount        bigint      NOT NULL,
  currency      char(3)     NOT NULL,
  status        text        NOT NULL,                 -- 'success', 'failed', 'reversed'
  occurred_at   timestamptz NOT NULL,
  UNIQUE (file_id, provider_ref)
);

CREATE INDEX settlement_records_ref_idx ON settlement_records (our_reference);

CREATE TABLE reconciliation_runs (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  file_id           uuid         NOT NULL REFERENCES settlement_files (id),
  status            recon_status NOT NULL DEFAULT 'running',
  started_at        timestamptz  NOT NULL DEFAULT now(),
  finished_at       timestamptz,
  matched_count     int          NOT NULL DEFAULT 0,
  discrepancy_count int          NOT NULL DEFAULT 0,
  error             text
);

CREATE TABLE reconciliation_discrepancies (
  id                   bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  run_id               uuid             NOT NULL REFERENCES reconciliation_runs (id),
  kind                 discrepancy_kind NOT NULL,
  transfer_id          uuid REFERENCES transfers (id),
  settlement_record_id bigint REFERENCES settlement_records (id),
  expected_amount      bigint,
  actual_amount        bigint,
  resolution           text NOT NULL DEFAULT 'open' CHECK (resolution IN ('open', 'resolved', 'ignored')),
  note                 text,
  created_at           timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX recon_discrepancies_run_idx ON reconciliation_discrepancies (run_id, resolution);

-- -----------------------------------------------------------------------------
-- Audit log (append-only lewat GRANT, lihat bagian akhir)
-- -----------------------------------------------------------------------------
CREATE TABLE audit_log (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  actor       text        NOT NULL,                   -- api client / 'system:reconciler'
  action      text        NOT NULL,                   -- 'account.freeze', 'discrepancy.resolve', ...
  entity_type text        NOT NULL,
  entity_id   text        NOT NULL,
  detail      jsonb       NOT NULL DEFAULT '{}'::jsonb,
  request_id  text,
  created_at  timestamptz NOT NULL DEFAULT now()
);

-- -----------------------------------------------------------------------------
-- Integrity views (dipakai /v1/admin/integrity dan test). Semuanya read-only.
-- Setiap baris yang muncul di view ini = BUG yang harus diselidiki.
-- -----------------------------------------------------------------------------

-- Saldo cache harus sama dengan hasil penjumlahan ledger.
CREATE VIEW v_balance_drift AS
SELECT a.id AS account_id,
       b.posted AS cached_posted,
       COALESCE(SUM(CASE WHEN e.direction::text = a.normal_side::text THEN e.amount ELSE -e.amount END), 0) AS derived_posted
  FROM accounts a
  JOIN account_balances b ON b.account_id = a.id
  LEFT JOIN ledger_entries e ON e.account_id = a.id
 GROUP BY a.id, b.posted
HAVING b.posted <> COALESCE(SUM(CASE WHEN e.direction::text = a.normal_side::text THEN e.amount ELSE -e.amount END), 0);

-- Transfer yang total debit-nya tidak sama dengan total kredit.
CREATE VIEW v_unbalanced_transfers AS
SELECT transfer_id,
       COALESCE(SUM(amount) FILTER (WHERE direction = 'debit'),  0) AS total_debit,
       COALESCE(SUM(amount) FILTER (WHERE direction = 'credit'), 0) AS total_credit
  FROM ledger_entries
 GROUP BY transfer_id
HAVING COALESCE(SUM(amount) FILTER (WHERE direction = 'debit'), 0)
    <> COALESCE(SUM(amount) FILTER (WHERE direction = 'credit'), 0);

-- Transfer berstatus posted tanpa entri ledger sama sekali.
CREATE VIEW v_posted_transfers_without_entries AS
SELECT t.id AS transfer_id
  FROM transfers t
  LEFT JOIN ledger_entries e ON e.transfer_id = t.id
 WHERE t.status = 'posted'
 GROUP BY t.id
HAVING COUNT(e.id) = 0;

-- -----------------------------------------------------------------------------
-- Seed: akun sistem (IDR)
-- -----------------------------------------------------------------------------
WITH sys AS (
  INSERT INTO accounts (kind, owner_ref, currency, normal_side, allow_negative) VALUES
    ('system_bank',     'system:bank',     'IDR', 'debit',  true),   -- kas di bank (asset)
    ('system_fee',      'system:fee',      'IDR', 'credit', false),  -- pendapatan biaya
    ('system_suspense', 'system:suspense', 'IDR', 'credit', true)    -- penampung selisih
  RETURNING id, allow_negative
)
INSERT INTO account_balances (account_id, allow_negative)
SELECT id, allow_negative FROM sys;

-- -----------------------------------------------------------------------------
-- Hak akses (pengganti trigger append-only)
-- -----------------------------------------------------------------------------
-- Model role:
--   ledger_migrator : pemilik semua objek dan menjalankan migration (CI/CD). Bukan superuser.
--   ledger_app      : dipakai API, relay, dan reconciler. BUKAN pemilik tabel.
-- Kedua role disediakan oleh platform/DBA (atau docker init script untuk dev lokal),
-- bukan oleh migration ini. Pemilik tabel selalu bisa UPDATE/DELETE, jadi aplikasi
-- tidak boleh connect sebagai pemilik.

GRANT USAGE ON SCHEMA public TO ledger_app;
GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO ledger_app;

-- Append-only: hanya SELECT dan INSERT (tidak ada UPDATE/DELETE/TRUNCATE)
GRANT SELECT, INSERT ON ledger_entries, audit_log, settlement_files, settlement_records TO ledger_app;

-- Boleh berubah, tetapi hanya kolom yang memang perlu berubah
GRANT SELECT, INSERT ON
  accounts, account_balances, transfers, holds, idempotency_keys, outbox_events,
  reconciliation_runs, reconciliation_discrepancies
TO ledger_app;

GRANT UPDATE (status)                                                      ON accounts                     TO ledger_app;
GRANT UPDATE (posted, held, version, updated_at)                           ON account_balances             TO ledger_app;
GRANT UPDATE (status, posted_at)                                           ON transfers                    TO ledger_app;
GRANT UPDATE (status, transfer_id, resolved_at)                            ON holds                        TO ledger_app;
GRANT UPDATE (status, response_code, response_body, locked_until)          ON idempotency_keys             TO ledger_app;
GRANT UPDATE (published_at, attempts, last_error)                          ON outbox_events                TO ledger_app;
GRANT UPDATE (status, finished_at, matched_count, discrepancy_count, error) ON reconciliation_runs         TO ledger_app;
GRANT UPDATE (resolution, note)                                            ON reconciliation_discrepancies TO ledger_app;

-- Pembersihan key idempotency yang sudah kedaluwarsa
GRANT DELETE ON idempotency_keys TO ledger_app;

GRANT SELECT ON v_balance_drift, v_unbalanced_transfers, v_posted_transfers_without_entries TO ledger_app;

COMMIT;
