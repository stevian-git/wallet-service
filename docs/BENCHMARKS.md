# Wallet Service benchmarks

This document captures load test results and chaos test evidence for the
wallet service. **All numeric values below are placeholders.** Run the tests on
the target hardware before filling them in.

## How to reproduce

```bash
# 1. Bring up the stack
make up
make migrate
make kafka-topic

# 2. Start the API and relay (separate terminals)
make run
make relay

# 3. Mixed load
make loadtest VUS=10 DURATION=60s
cat results/summary.json

# 4. Hot-wallet load (80% to one destination)
make loadtest-hot VUS=20 DURATION=120s
cat results/summary.json

# 5. Chaos test (runs load + relay internally)
make chaos
ls results/chaos-*/
```

k6 must be installed (`brew install k6` on macOS).

## Hardware

| Field | Value |
|---|---|
| Host | `<fill after run>` |
| CPU | `<fill after run>` |
| Memory | `<fill after run>` |
| OS | `<fill after run>` |
| Go version | `<fill after run>` |
| Docker engine | `<fill after run>` |
| PostgreSQL | 16-alpine (single container, default shared_buffers) |
| Redpanda | v25.2.4 (single node, 1 CPU / 1 GB memory) |
| k6 version | `<fill after run>` |

## Mixed load results

Run: `make loadtest VUS=10 DURATION=60s`

| Metric | Value |
|---|---|
| Total requests | `<fill after run>` |
| Request rate (req/s) | `<fill after run>` |
| HTTP duration avg (ms) | `<fill after run>` |
| HTTP duration p50 (ms) | `<fill after run>` |
| HTTP duration p95 (ms) | `<fill after run>` |
| HTTP duration p99 (ms) | `<fill after run>` |
| Transfer outcome mix | `<fill after run>` |
| Outbox lag max (s) | `<fill after run>` |
| Lock wait p95 (ms) | `<fill after run>` |
| Idempotency replay rate | `<fill after run>` |

## Hot-wallet load results

Run: `make loadtest-hot VUS=20 DURATION=120s`

| Metric | Value |
|---|---|
| Total requests | `<fill after run>` |
| Request rate (req/s) | `<fill after run>` |
| HTTP duration avg (ms) | `<fill after run>` |
| HTTP duration p50 (ms) | `<fill after run>` |
| HTTP duration p95 (ms) | `<fill after run>` |
| HTTP duration p99 (ms) | `<fill after run>` |
| Lock wait p95 (ms) | `<fill after run>` |
| Outbox lag max (s) | `<fill after run>` |

The hot-wallet scenario concentrates lock contention on one destination
account, surfacing `wallet_lock_wait_seconds` and lock-related tail latencies.

## Chaos evidence

Run: `make chaos`

| Check | Expected | Actual |
|---|---|---|
| `/v1/admin/integrity` ok | `true` | `<fill after run>` |
| Posted transfers (non-reversal) | — | `<fill after run>` |
| Published `transfer.posted` events | equal to posted | `<fill after run>` |
| Outbox drain time (s) | — | `<fill after run>` |
| Disruption timeline | pg terminate → kafka 30s stop → relay kill/restart | see `results/chaos-<timestamp>/relay.log` |

The assertion uses a single-statement snapshot of posted transfers and
published outbox events, so the two counts are read from one consistent
Postgres read. `published_at IS NOT NULL` only holds after the relay
successfully `ProduceSync`'d the record to Kafka, so this also validates that
the broker recovered and the relay drained the backlog.

## Notes

- Transient 5xx during Postgres backend termination are expected and do not
  violate the integrity invariant (uncommitted transactions are rolled back).
- The relay is at-least-once; a crash between `ProduceSync` and commit can
  produce duplicate Kafka records. The posted-vs-published assertion counts
  DB rows, so duplicates in Kafka are invisible to the assertion.
- Numbers in this file must be filled in from actual runs on the target
  machine. They have been intentionally left as placeholders.
