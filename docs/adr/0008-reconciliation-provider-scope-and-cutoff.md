# ADR 0008: Reconciliation scope per provider and settlement cut-off

## Status

Accepted. Implemented in migration `0003_settlement_providers`.

## Context

A reconciliation run compares one settlement file against ledger transfers.
Before this ADR, the scope (ADR 0006) had two gaps that produce false
discrepancies at scale:

1. **No provider scope.** Every posted, non-reversal transfer with a reference,
   created on the settlement date, was in scope. With two providers settling
   the same day, reconciling provider A's file flagged all of provider B's
   transfers as `missing_in_settlement`. Transfers did not record which provider
   they belong to.
2. **UTC day boundary.** Scope was `[settlement_date 00:00 UTC, +1 day)`. Banks
   and e-wallets in Indonesia usually cut off at local time (WIB, UTC+7) or at
   a provider-specific hour (for example 23:00 WIB). Transfers between 00:00 and
   07:00 WIB fell into the previous UTC day. They then showed up as
   `missing_in_settlement` in one run and `missing_in_ledger` in the next.

## Options considered

### Provider scope

- **A. `provider` column on `transfers`**, set by the API, plus an index.
  - Pro: explicit and cheap to query.
  - Con: every money-moving endpoint and existing rows change, and the column
    restates what the double entry already says.
- **B. Provider derived from the reference prefix**, mapped in config.
  - Pro: no schema change.
  - Con: depends on reference conventions that clients control, and it breaks
    silently when a prefix changes.
- **C. Provider derived from the system account the transfer posts against**,
  with one `system_bank` account per provider and currency.
  - Pro: uses the existing double-entry model. Top-ups and withdrawals already
    post against a bank account.
  - Con: peer transfers have no provider.

### Cut-off

- **D. `cutoff_tz` and `cutoff_time` per provider**, in a
  `settlement_providers` table.
- **E. Scope by the settlement records' own `occurred_at` range**, with a
  tolerance window. This adapts to each file, but it misclassifies transfers
  near the boundary when a file has no records there.

## Decision

**C for the provider scope and D for the cut-off.**

### Providers and their bank accounts

- **Tables:**
  - `settlement_providers (provider PK, cutoff_tz, cutoff_time)`
  - `settlement_provider_accounts (provider, currency, account_id)`, with
    PK `(provider, currency)`, `account_id` UNIQUE, and a composite FK to
    `accounts (id, currency)` so the account's currency matches
- **Registration:** `PUT /v1/admin/settlement-providers/{provider}`.
  - Validation happens in Go: the provider name pattern, the IANA zone via
    `time.LoadLocation` (with `time/tzdata` embedded), `HH:MM`, and the
    currencies.
  - The upsert locks the provider row. For each new currency, the PUT creates a
    `system_bank` account with `owner_ref = 'provider:<name>'`, debit-normal and
    allowed to go negative like `system:bank`, and links it. It also writes
    `audit_log`, all in one transaction.
  - Repeating the PUT changes nothing, so it needs no `Idempotency-Key`; it
    moves no money.
- **Currencies are only added.** An account with ledger history is never
  unlinked. A provider that stops supporting a currency keeps its account, and
  the account simply stops receiving postings.
- **Top-ups and withdrawals** take an optional `provider`. With a provider they
  post against that provider's account for the currency. An unknown provider
  or currency returns `422 unknown_provider`. Without a provider they post
  against the shared `system:bank` account, as before.

### Scope of a run

Inside the run's REPEATABLE READ transaction, the file's provider is looked up:

- **Registered provider:**
  - The transfers are those posted (source or destination) against one of the
    provider's accounts, with `created_at` in the provider's business day.
  - **Business day D:** `[D-1 at cutoff_time, D at cutoff_time)` in
    `cutoff_tz`. For example, 23:00 Asia/Jakarta is
    `[D-1 16:00Z, D 16:00Z)`.
  - **A cut-off of `00:00`** means the midnight at the end of D, so the window
    is the local calendar day D.
  - **Early cut-offs:** any other cut-off ends day D on D itself. A cut-off
    such as `00:30` therefore makes day D mostly the previous calendar day.
    Real cut-offs are evening hours.
  - A registered provider with no accounts has an empty transfer scope. Every
    record is then reported as `missing_in_ledger`, which is the correct signal.
- **Unregistered provider:** the scope from before this ADR (every referenced
  transfer, UTC calendar day). Existing files and clients keep working.

Each completed run stores `scope_start`, `scope_end`, and `scope_provider`
(NULL means legacy scope). Changing a provider's cut-off therefore never makes
an earlier run's result ambiguous.

The transfer cursor keeps the `COLLATE "C"` order from ADR 0006. The account
filter is
`source_account_id = ANY($accounts) OR destination_account_id = ANY($accounts)`,
which the existing source and destination indexes serve.

## Consequences

- **Fewer false discrepancies.** Several providers can settle on the same date,
  and transfers near midnight land in the right business day.
- **Peer transfers are out of every provider's scope.** Wallet-to-wallet
  transfers are not settled by a provider, so this is correct.
- **Hold captures** are in scope only if they post against a provider account,
  which they do not today.
- **Clients must send `provider`** on top-ups and withdrawals that a provider
  settles. Transfers posted against `system:bank` are only reconciled by files
  of unregistered providers, under the legacy scope.
- **Registering a provider changes the scope of later runs.** Earlier runs keep
  the scope they recorded.
