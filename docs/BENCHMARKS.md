# Wallet Service benchmarks

This document captures load test results and chaos test evidence for the
wallet service. The numbers come from runs on **2026-11-17** on a developer
laptop (the chaos run from **2026-11-18**): one Postgres container, one
single-node Redpanda, the API and relay as native binaries, and k6 on the same
machine. They show how the system behaves and where the bottlenecks are. They are not production capacity figures.

## How to reproduce

```bash
# 1. Bring up the stack on a clean database
make reset        # deletes the local volumes
make up
make migrate
make kafka-topic
make build

# 2. Start the API and relay (separate terminals). Prometheus scrapes :8080.
make run
make relay

# 3. Mixed load
make loadtest VUS=10 DURATION=60s
cat results/summary.json

# 4. Hot-wallet load (80% to one destination)
make loadtest-hot VUS=20 DURATION=120s
cat results/summary.json

# 5. Chaos test (stop the relay from step 2 first; the script starts its own)
make chaos
ls results/chaos-*/
```

k6 must be installed (`brew install k6` on macOS).

The k6 summary has request counts and latency. The other metrics come from
the API's Prometheus metrics, either queried over the run window in Prometheus
or diffed from `/metrics` before and after a run:

| Metric | Source |
|---|---|
| Lock wait p95 | `histogram_quantile(0.95, sum by (le) (increase(wallet_lock_wait_seconds_bucket[<window>])))` |
| Outbox lag max | `max_over_time(wallet_outbox_lag_seconds[<window>])` |
| Transfer outcome mix | `wallet_transfers_total` by `type` and `outcome` |
| Idempotency replays | `wallet_idempotency_replays_total` |

## Hardware

| Field | Value |
|---|---|
| Host | Apple Mac17,3 laptop |
| CPU | Apple M5, 10 cores |
| Memory | 16 GB |
| OS | macOS 26.5.2 |
| Go version | go1.26.4 darwin/arm64 |
| Docker engine | 29.5.3 |
| PostgreSQL | 16-alpine (single container, default shared_buffers) |
| Redpanda | v25.2.4 (single node, 1 CPU / 1 GB memory) |
| k6 version | v2.4.0 |

## Mixed load results

Run: `make loadtest VUS=10 DURATION=60s`. 20 wallets, 90% transfers between
two random wallets and 10% top-ups. Each VU sleeps 50 ms between iterations,
so the request rate is capped at about 200 req/s.

The first run started from a freshly migrated database. The second ran after
the hot-wallet run had loaded the database further.

| Metric | Run 1 (fresh DB) | Run 2 |
|---|---|---|
| Total requests | 10,227 | 10,360 |
| Request rate (req/s) | 170.1 | 172.4 |
| HTTP duration avg (ms) | 8.35 | 7.68 |
| HTTP duration p50 (ms) | 6.32 | 6.47 |
| HTTP duration p95 (ms) | 14.22 | 13.80 |
| HTTP duration p99 (ms) | 49.46 | 21.13 |
| HTTP duration max (ms) | 329.47 | 258.83 |
| Transfer outcome mix | 9,166 transfers + 1,041 top-ups posted, 0 rejected | 9,316 transfers + 1,004 top-ups posted, 0 rejected |
| Outbox lag max (s) | 1.00 | 0.88 |
| Lock wait p95 (ms) | ~7.4 | ~7.2 |
| Idempotency replay rate | 0 | 40 / 10,360 (0.39%) |

- **Status codes:** every response was `201`.
- **Replays in run 2:** the setup step reuses its idempotency keys, so the
  20 account creates and 20 top-ups were replayed, not posted again. Hence
  `1,024` top-up requests but `1,004` posted top-ups. This is the expected
  idempotency behaviour.
- **Lock wait p95:** interpolated inside the 5–10 ms histogram bucket, so it is
  approximate.
- **Outbox lag:** at most about the relay poll interval (`OUTBOX_POLL_INTERVAL=1s`).

## Hot-wallet load results

Run: `make loadtest-hot VUS=20 DURATION=120s`. 80% of transfers credit wallet 0.

| Metric | Value |
|---|---|
| Total requests | 44,436 |
| Request rate (req/s) | 370.0 |
| HTTP duration avg (ms) | 3.86 |
| HTTP duration p50 (ms) | 2.66 |
| HTTP duration p95 (ms) | 7.42 |
| HTTP duration p99 (ms) | 25.39 |
| HTTP duration max (ms) | 181.30 |
| Transfer outcome mix | 39,917 transfers + 4,479 top-ups posted, 0 rejected |
| Lock wait p95 (ms) | ~4.8 |
| Outbox lag max (s) | 0.98 |

The hot-wallet scenario concentrates lock contention on one destination
account, surfacing `wallet_lock_wait_seconds` and lock-related tail latencies.

At these rates the hot wallet was **not** the bottleneck. Latency and lock
wait were lower than in the mixed run, even with twice the VUs.

The cause has not been investigated. One plausible explanation is that in the
mixed run every transfer locks two random rows out of 20, so waits can form
chains. In the hot run most transfers queue on one row. Finding the
saturation point of the hot wallet needs a run without the 50 ms think time.

## Chaos evidence

Run: `make chaos` (mixed load, 10 VUs, 120 s) on **2026-11-18**, on a freshly
reset database, with exactly one relay process alive at any time (see
[Earlier runs](#earlier-runs-relay-kill-not-isolated)).

| Check | Expected | Actual |
|---|---|---|
| `/v1/admin/integrity` ok | `true` | `true` (no balance drift, no unbalanced transfers, no posted transfer without entries) |
| Posted transfers (non-reversal) | — | 20,494 |
| Published `transfer.posted` events | equal to posted | 20,494 |
| Kafka records vs outbox rows | ≥ outbox rows (at-least-once) | 20,515 = 20,515 (no duplicates in this run) |
| Events published by the restarted relay | the backlog plus the rest of the run | 6,732 |
| Outbox drain time (s) | — | < 1 (drained at the first check after k6 finished) |
| HTTP 5xx during the run | only around the Postgres termination | 10 of 20,522 requests |
| Load during chaos | — | 170.8 req/s, p95 14.86 ms, p99 19.50 ms |
| Disruption timeline | pg terminate → kafka 30s stop → relay kill/restart | see `results/chaos-<timestamp>/relay.log` |

Outbox rows are the 20,494 `transfer.posted` events plus 21 `account.created`
events: 20 from the k6 setup and 1 from a smoke check before the run. The
posted count also includes that smoke check's two top-ups.

Timeline of the recorded run (UTC):

| Time | Event |
|---|---|
| 07:53:53 | relay and k6 started |
| 07:53:59 | all Postgres backends terminated |
| 07:54:19 | Redpanda stopped |
| 07:54:49 | Redpanda started (healthy at 07:54:51) |
| 07:55:15 | last event published by the first relay |
| 07:55:16 | relay killed with SIGKILL |
| 07:55:26 | relay restarted; first event published |
| 07:55:31–32 | restarted relay published ~2,700 events in 2 s (the backlog), then followed the load at ~180/s |
| 07:55:56 | k6 finished; outbox drained; integrity and counts asserted |

- **While Redpanda was down**, the relay logged no errors. `ProduceSync` kept
  retrying inside the client, and publishing resumed when the broker came back.
- **After the restart**, the new relay published one event, then paused for
  about 5 s before draining the backlog. The cause of the pause has not been
  investigated. The likely cause is the Kafka client's connection and producer
  initialisation.

The assertion uses a single-statement snapshot of posted transfers and
published outbox events, so the two counts are read from one consistent
Postgres read. `published_at IS NOT NULL` only holds after the relay
successfully `ProduceSync`'d the record to Kafka, so this also validates that
the broker recovered and the relay drained the backlog.

### Earlier runs: relay kill not isolated

The runs from 2026-11-17 both passed, but neither tested the relay crash
cleanly:

1. **First run.** The script started the relay with `go run` and killed that
   PID. Killing `go run` leaves the compiled relay running as an orphan, so
   the relay kill never happened. The log showed a single
   `outbox relay started`.
2. **Second run** (after switching to `bin/relay` and SIGKILL). The relay that
   the first run's restart step had started with `go run` was still running as
   an orphan. It kept publishing while the script's relay was down. The
   script's post-kill check missed it, because it matched the `bin/relay` path
   and the orphan ran from the Go build cache. The integrity and
   posted-vs-published results of that run hold, but the relay restart was not
   what drained the backlog.

`scripts/chaos.sh` now:
- starts the built `bin/relay` (`RELAY_BIN`)
- refuses to start if any process named `relay` is already running
- kills the relay with SIGKILL
- fails if any process named `relay` survives the kill

The run recorded above uses that script. Its log shows two relay starts, with
no publishes between the kill and the restart.

## Notes

- Transient 5xx during Postgres backend termination are expected and do not
  violate the integrity invariant (uncommitted transactions are rolled back).
- The relay is at-least-once. A crash between `ProduceSync` and commit can
  produce duplicate Kafka records. The posted-vs-published assertion counts DB
  rows, so duplicates in Kafka are invisible to it. The Kafka high-watermark
  comparison above is what shows whether duplicates occurred. In this run the
  SIGKILL landed between batches, so there were none.
- Re-run on the target hardware before quoting numbers for capacity planning.
