# ADR-0005: Pessimistic hold locking with a fixed lock order

- Status: Accepted
- Date: 2026-10-26

## Context

A hold reserves part of an account's available balance. While a hold is active:

- available balance is `posted - held`;
- a transfer or withdrawal may use only the remaining available balance;
- capture, release, and expiry all reduce `held` and resolve the hold exactly
  once;
- capture also posts a `hold_capture` transfer to a destination account.

Concurrent operations can otherwise:

- let two holds reserve the same funds;
- let a transfer spend funds already reserved by a hold;
- let capture and expiry both resolve the same hold;
- deadlock by locking the hold and account rows in different orders.

## Options considered

### A. Optimistic version checks

Read the balance and hold versions, compute the new state, and update with a
`WHERE version = $expected` guard.

Advantages:
- No long row locks.
- Good under low contention.

Disadvantages:
- Every conflicting operation must retry the whole business transaction.
- Correctness depends on every caller handling conflicts and retries.
- Capture touches a hold, two balances, a transfer, entries, outbox rows, and
  idempotency completion. Retrying all of that is more error-prone than blocking
  briefly.

### B. Pessimistic row locks with a fixed order (chosen)

Use PostgreSQL row locks and deterministic ordering.

Advantages:
- A conflicting capture/release/expire waits and then observes the committed
  state.
- The second resolver sees a non-active hold and returns `409` instead of
  applying the hold twice.
- The existing `Post` path already locks account balances with
  `ORDER BY account_id ... FOR UPDATE`.

Disadvantages:
- Transactions can wait under contention.
- A missed lock-order rule can introduce deadlocks, so ordering must be tested.

### C. Serializable isolation with retries

Advantages:
- Strong database-level guarantees.

Disadvantages:
- Requires serialization-failure retries around the entire request.
- Does not remove the need for an explicit order in practice.

## Decision

Use option B.

- A hold's row is the first lock for `capture`, `release`, and `expire`.
- Account balance rows are locked next with the existing deterministic
  `ORDER BY account_id ... FOR UPDATE`.
- `create hold` locks only the account balance row, checks
  `posted - held >= amount`, then increments `held`.
- `capture` locks the hold, verifies it is active and not expired, converts the
  requested amount (zero means full hold), locks source and destination
  balances in sorted order, decrements the full held amount, posts a
  `hold_capture` transfer for the captured amount, and marks the hold captured.
  Any uncaptured remainder is therefore released in the same transaction.
- `release` and `expire` lock the hold, decrement `held` by the full hold
  amount, and set `released` or `expired` with `resolved_at`.
- Releasing a hold whose `expires_at` has passed is allowed while it is still
  active; the funds are freed and the hold becomes `released`. Capturing such a
  hold is rejected with `hold_expired`. Once any resolver sets a terminal
  status, later operations return `hold_not_active`.
- The expiry worker claims one due hold at a time with
  `FOR UPDATE SKIP LOCKED`, so multiple workers do not process the same hold.
- No schema change was required for this phase.

## Consequences

- Hold operations serialize on the hold row and then on account rows; transfer
  operations continue to serialize on account rows only.
- Lock order is part of the correctness contract. New hold queries must not lock
  accounts before the hold row.
- The `account_balances` check constraint and the held-balance guard are
  backstops. A hold exceeding `held` is treated as ledger inconsistency rather
  than a normal client error.
