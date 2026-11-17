# ADR 0008: Reconciliation scope per provider and settlement cut-off

## Status

Proposed. Not implemented.

## Context

A reconciliation run compares one settlement file against ledger transfers. The
current scope (ADR 0006) has two gaps that produce false discrepancies at scale:

1. **No provider scope.** Every posted, non-reversal transfer with a reference,
   created on the settlement date, is in scope. With two providers settling the
   same day, reconciling provider A's file flags all of provider B's transfers
   as `missing_in_settlement`. Transfers do not record which provider they
   belong to.
2. **UTC day boundary.** Scope is `[settlement_date 00:00 UTC, +1 day)`. Banks
   and e-wallets in Indonesia usually cut off at local time (WIB, UTC+7) or at a
   provider-specific hour (for example 23:00 WIB). Transfers between 00:00 and
   07:00 WIB fall into the previous UTC day. They then show up as
   `missing_in_settlement` in one run and `missing_in_ledger` in the next.
   `settlement_records.occurred_at` is imported but never used.

## Options

### Provider scope

- **A. `provider` column on `transfers`** (nullable text, set by the API from the
  request or from the account/channel), plus an index on
  `(provider, created_at) WHERE reference IS NOT NULL`. The scope query filters
  `provider = settlement_files.provider`.
  - Pro: explicit and cheap to query.
  - Con: `openapi.yaml` (request field) and every money-moving endpoint change.
    Existing rows stay `NULL` and need a backfill rule.
- **B. Provider derived from the reference prefix** (for example `BCA-...`),
  mapped in config.
  - Pro: no schema change.
  - Con: depends on reference conventions that clients control. It breaks
    silently when a prefix changes.
- **C. Provider derived from the system account the transfer posts against**
  (one `system_bank` account per provider).
  - Pro: uses the existing double-entry model. Topups and withdrawals already
    post against a system account.
  - Con: needs one system account per provider, and peer transfers have no
    provider.

### Cut-off

- **D. `cutoff_tz` and `cutoff_time` per provider** (a `settlement_providers`
  config table or config file). The scope becomes
  `[date + cutoff - 1 day, date + cutoff)` in that time zone.
- **E. Scope by the settlement records' own `occurred_at` range**, with a
  tolerance window (for example ±2h). This adapts to each file, but a file with
  no records near the boundary still misclassifies edge transfers.

## Recommendation (to be decided)

- **C for topup/withdrawal**, which are the flows that a provider actually
  settles, **combined with A** once a client-supplied channel is needed.
- **D for the cut-off**: a small `settlement_providers` table
  (`provider PK, cutoff_tz, cutoff_time`), so the scope is explicit and
  testable.

Both need a migration and an OpenAPI change, and require approval per AGENT.md.

## Consequences

- Until this is decided, operators should expect `missing_in_settlement` noise
  when several providers settle on the same date, and near-midnight
  discrepancies.
