# ADR-0004: Transactional outbox and relay instead of dual writes

- Status: Accepted
- Date: 2026-10-26

## Context

The service must publish ledger events (`account.created`, `transfer.posted`,
`transfer.reversed`, `hold.created`, `hold.captured`, `hold.released`,
`hold.expired`) to Kafka without losing events or inventing events that were
never committed.

A naive dual write does two independent operations:

1. commit the ledger transaction to PostgreSQL;
2. publish the event to Kafka.

Either step can fail after the other succeeds. Publishing before commit can emit
an event for a rolled-back transfer. Committing before publishing can leave a
committed transfer with no event. Retrying blindly can duplicate events.

The schema already contains `outbox_events` and the ledger write path already
inserts an event row in the same transaction as the balance and entry changes.

## Options considered

### A. Dual write from the request path

Publish to Kafka immediately before or after the database commit.

Advantages:
- Fewer moving parts; no relay process.

Disadvantages:
- Cannot be atomic across PostgreSQL and Kafka.
- Failure handling is ad hoc and differs per endpoint.
- A Kafka outage either fails money movement or silently loses events.

### B. Poll the outbox and publish with a relay (chosen)

Keep the outbox insert inside the ledger transaction. A separate relay reads
unpublished rows, publishes them, and marks them published.

Advantages:
- The event and the ledger mutation commit or roll back together.
- Kafka availability does not affect the money path.
- Retry state (`attempts`, `last_error`) is durable and observable.
- Multiple relay instances can share the work through row locks.

Disadvantages:
- Publication is asynchronous; consumers see events after the commit.
- Delivery is at-least-once, so consumers must dedupe by `event_id`.
- A stuck event blocks later events for the same aggregate. This is intentional
  to preserve per-aggregate order.

### C. Change data capture (CDC)

Read the PostgreSQL WAL with Debezium or a similar tool.

Advantages:
- No relay code in this service.
- Strong ordering from the WAL.

Disadvantages:
- Adds a large operational dependency and connector configuration.
- The event contract would be coupled to the database representation.
- The outbox table already exists and gives an explicit application-level
  contract.

## Decision

Use option B.

- The ledger inserts the outbox row in the same transaction as the transfer,
  entries, balances, hold, and idempotency completion.
- The relay claims rows with `published_at IS NULL` and `FOR UPDATE SKIP
  LOCKED`.
- Per aggregate, only the earliest unpublished row is eligible:
  `NOT EXISTS (... p.id < e.id AND p.published_at IS NULL)`. This keeps
  `account.created` before later account events and keeps a transfer's event
  before its reversal event even with multiple relays.
- Kafka key is `aggregate_id`, so events for one aggregate stay on one
  partition.
- The relay marks `published_at` only after the broker acknowledges the record.
  A crash after publish but before commit re-publishes the event.
- Failures increment `attempts`, store a truncated `last_error`, and are retried
  after exponential backoff with jittered operational spacing at the process
  level.
- The consumer contract includes a stable `event_id` header. Consumers must
  dedupe; the demo consumer uses an in-memory TTL map and explicitly documents
  that this is not durable.
- Local development uses Redpanda. The relay also allows Kafka auto topic
  creation; production should create `ledger.events.v1` with the desired
  partition count.

## Consequences

- Event delivery is at-least-once, not exactly-once.
- A permanent publish failure blocks later events for that aggregate while other
  aggregates continue. Operators can inspect `outbox_events.last_error`.
- `outbox_events` is operational state, not the ledger. It can be archived after
  publication without changing ledger history.
- No schema change was required for this phase.
