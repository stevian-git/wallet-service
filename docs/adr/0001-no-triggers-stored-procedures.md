# ADR-0001: Keep business logic in Go; no triggers or stored functions

- Status: Accepted
- Date: 2026-10-26

## Context

The ledger has invariants that must never be violated:

1. Money is `bigint` minor units.
2. `ledger_entries` and `audit_log` are append-only.
3. Every transfer is balanced (sum of debits equals sum of credits).
4. `account_balances` is a cache updated in the same transaction as the ledger
   entries.
5. Multiple account balances are locked in a deterministic order.
6. Money-moving HTTP requests are idempotent.
7. An account without `allow_negative` never has a negative available balance.

Some of these can be expressed in SQL with triggers or stored functions. The
question is whether the database should be the second implementation of business
logic, or whether the Go service should own it.

## Options considered

### A. Enforce invariants in the database (triggers/functions)

Examples: a trigger that recomputes `account_balances` after every ledger insert,
a deferred constraint trigger that checks debit/credit totals at commit, or a
stored procedure that performs a posting.

Advantages:
- Any writer, including an operator with a SQL prompt, gets the same guarantees.
- Some invariants can be enforced atomically by PostgreSQL itself.
- The application can fail fast and trust the database.

Disadvantages:
- Business logic is split between Go and PL/pgSQL, which doubles the code and
  the test surface.
- Trigger behaviour is implicit: understanding one transfer requires reading Go
  code, trigger code, and the execution order.
- Migration and debugging require a higher database privilege level and make
  deployments harder for a non-superuser migration role.
- The ledger also needs application-level decisions (idempotency replay,
  insufficient-funds error mapping, locking order, event publication) that
  triggers cannot own, so the Go code still has to understand the whole flow.
- Error mapping becomes opaque: a trigger exception must be translated back into
  a domain error, often by parsing constraint names and SQLSTATEs.

### B. Declarative constraints plus Go business logic (chosen)

The schema contains only:
- primary keys, foreign keys, unique indexes, and `CHECK` constraints;
- read-only integrity views;
- `GRANT`s that separate the migration owner from the application role.

All posting, balance calculation, locking order, hold handling, idempotency, and
reversal logic lives in Go.

Advantages:
- One implementation of the business rules and one test suite for them.
- The complete posting path is explicit and reviewable in a single place.
- Migrations only need ordinary DDL privileges and can run as a non-superuser
  role (`ledger_migrator`).
- Domain errors (`ErrInsufficientFunds`, `ErrCurrencyMismatch`, etc.) are
  produced directly, without translating database exceptions.
- The schema remains easy to reason about and portable to other SQL databases if
  the views/queries are adapted.

Disadvantages and trade-offs:
- The database cannot independently prove that a balanced posting was inserted.
  A future writer that bypasses the Go service could write an unbalanced
  transfer if it had the privileges to do so. This is mitigated by the
  append-only write model and by restricting access to the application role.
- Correctness depends on every mutation going through `internal/ledger.Post`.
  Code review and package boundaries must protect that entry point.
- The balance cache can drift from the ledger if there is a bug. The schema
  therefore includes `v_balance_drift`,
  `v_unbalanced_transfers`, and `v_posted_transfers_without_entries`, and the
  service exposes `GET /v1/admin/integrity`. Tests assert that all three views
  are empty after concurrent and randomized workloads.
- Append-only is enforced by privileges, not by a trigger. The application role
  (`ledger_app`) has only `SELECT` and `INSERT` on `ledger_entries` and
  `audit_log`; it has no `UPDATE`, `DELETE`, or `TRUNCATE`. The application must
  never connect as the table owner. The API checks its current database role at
  startup and refuses to run as the owner.
- Insufficient funds are decided in Go after locking the balance rows. The
  `CHECK (allow_negative OR posted - held >= 0)` constraint is deliberately only
  a backstop. If SQLSTATE `23514` fires, the service logs it as an error (it
  indicates a bug or a missed race) and maps it to `ErrInsufficientFunds`.

## Decision

Use option B. The database owns structure, referential integrity, and access
control. The Go service owns business logic, posting, locking, and error
semantics. No triggers, stored functions, or stored procedures are allowed.

## Consequences

- Every mutation of the ledger must be a new transfer with balanced entries. A
  correction is a new reversing transfer; the original entries are never
  updated or deleted.
- Any new writer must use the same posting routine or reproduce its guarantees.
  Package boundaries keep the posting routine in `internal/ledger`.
- The integrity views and property/concurrency tests are not optional extras;
  they are the compensating controls for moving the invariant enforcement into
  application code.
- If a database-level invariant is ever required by an external auditor, it must
  be proposed as a new ADR and a migration, not added quietly.
