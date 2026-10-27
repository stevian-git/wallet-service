#!/usr/bin/env bash
#
# Chaos test: runs load + outbox relay, injects Postgres/Kafka/relay failures,
# waits for the system to recover, then asserts /v1/admin/integrity is ok
# and published outbox counts equal posted transfer counts.
#
# Requirements:
#   docker compose stack (postgres + redpanda) running
#   API already running on BASE_URL
#   k6 installed on PATH
#   jq installed
#   psql installed (for integrity/count queries)
#
# Usage:
#   ./scripts/chaos.sh
#
# Environment overrides (defaults shown):
#   BASE_URL=http://localhost:8080
#   API_KEY=dev_api_key
#   POSTGRES_PORT=5436
#   LOAD_DURATION=120   (seconds k6 runs)
#   LOAD_VUS=10
#   LOAD_HOT=0          (set to 1 for hot-wallet scenario)
#

set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
API_KEY="${API_KEY:-dev_api_key}"
POSTGRES_PORT="${POSTGRES_PORT:-5436}"
LOAD_DURATION="${LOAD_DURATION:-120}"
LOAD_VUS="${LOAD_VUS:-10}"
LOAD_HOT="${LOAD_HOT:-0}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

RELAY_PID=""
K6_PID=""
RESULTS_DIR="$ROOT_DIR/results/chaos-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$RESULTS_DIR"

cleanup() {
  local exit_code=$?
  echo
  echo "=== cleanup ==="
  if [[ -n "$K6_PID" ]] && kill -0 "$K6_PID" 2>/dev/null; then
    echo "stopping k6 (pid $K6_PID)"
    kill "$K6_PID" 2>/dev/null || true
    wait "$K6_PID" 2>/dev/null || true
  fi
  if [[ -n "$RELAY_PID" ]] && kill -0 "$RELAY_PID" 2>/dev/null; then
    echo "stopping relay (pid $RELAY_PID)"
    kill "$RELAY_PID" 2>/dev/null || true
    wait "$RELAY_PID" 2>/dev/null || true
  fi
  # Ensure Kafka is running after chaos
  docker compose -f "$ROOT_DIR/docker-compose.yml" start redpanda 2>/dev/null || true
  echo "cleanup done (exit=$exit_code)"
  exit "$exit_code"
}
trap cleanup EXIT

timestamp() { date -u +"%Y-%m-%dT%H:%M:%SZ"; }

log() { echo "[$(timestamp)] $*"; }

wait_for_api() {
  local timeout="${1:-30}"
  local start
  start=$(date +%s)
  while ! curl -fsS -o /dev/null -H "X-API-Key: $API_KEY" "$BASE_URL/healthz" 2>/dev/null; do
    if (( $(date +%s) - start >= timeout )); then
      log "ERROR: API not ready after ${timeout}s"
      return 1
    fi
    sleep 1
  done
  log "API ready"
}

wait_for_kafka_healthy() {
  local timeout="${1:-60}"
  local start
  start=$(date +%s)
  while ! docker compose -f "$ROOT_DIR/docker-compose.yml" exec -T redpanda \
    rpk cluster health 2>/dev/null | grep -q 'Healthy:.*true'; do
    if (( $(date +%s) - start >= timeout )); then
      log "ERROR: Kafka not healthy after ${timeout}s"
      return 1
    fi
    sleep 2
  done
  log "Kafka healthy"
}

wait_for_outbox_drain() {
  local timeout="${1:-180}"
  local start
  start=$(date +%s)
  while :; do
    local pending
    pending=$(psql -h localhost -p "$POSTGRES_PORT" -U ledger_app -d wallet -tAc \
      "SELECT count(*) FROM outbox_events WHERE published_at IS NULL;" 2>/dev/null || echo "-1")
    if [[ "$pending" == "0" ]]; then
      log "outbox drained"
      return 0
    fi
    if (( $(date +%s) - start >= timeout )); then
      log "ERROR: outbox not drained after ${timeout}s (pending=$pending)"
      return 1
    fi
    log "waiting for outbox drain (pending=$pending)..."
    sleep 2
  done
}

check_integrity() {
  local response
  response=$(curl -fsS -H "X-API-Key: $API_KEY" "$BASE_URL/v1/admin/integrity")
  echo "$response" > "$RESULTS_DIR/integrity-final.json"
  local ok
  ok=$(echo "$response" | jq -r '.ok')
  if [[ "$ok" != "true" ]]; then
    log "ERROR: integrity ok=$ok"
    echo "$response" | jq .
    return 1
  fi
  log "integrity ok=true"
}

check_event_consistency() {
  # Single-statement snapshot: one consistent read of both counts.
  local row
  row=$(psql -h localhost -p "$POSTGRES_PORT" -U ledger_app -d wallet -tAc "
    SELECT
      (SELECT count(*) FROM transfers WHERE status='posted' AND type <> 'reversal') AS posted,
      (SELECT count(*) FROM outbox_events
         WHERE event_type='transfer.posted' AND published_at IS NOT NULL) AS published;
  ")
  local posted published
  posted=$(echo "$row" | cut -d'|' -f1 | tr -d ' ')
  published=$(echo "$row" | cut -d'|' -f2 | tr -d ' ')
  log "posted transfers (non-reversal) = $posted"
  log "published transfer.posted events = $published"
  echo "{\"posted\": $posted, \"published\": $published}" > "$RESULTS_DIR/consistency.json"
  if [[ "$posted" != "$published" ]]; then
    log "ERROR: posted=$posted != published=$published"
    return 1
  fi
  log "posted == published"
}

# --- preflight ---------------------------------------------------------------

log "preflight"
wait_for_api 30
wait_for_kafka_healthy 30

# Baseline integrity
curl -fsS -H "X-API-Key: $API_KEY" "$BASE_URL/v1/admin/integrity" > "$RESULTS_DIR/integrity-baseline.json" || true

# --- start background processes ---------------------------------------------

log "starting outbox relay"
DATABASE_URL="postgres://ledger_app:app_dev_pw@localhost:$POSTGRES_PORT/wallet?sslmode=disable" \
KAFKA_BROKERS="localhost:9092" \
KAFKA_TOPIC="ledger.events.v1" \
OUTBOX_BATCH_SIZE="50" \
OUTBOX_POLL_INTERVAL="1s" \
OUTBOX_MAX_BACKOFF="30s" \
  go run -C "$ROOT_DIR" ./cmd/relay > "$RESULTS_DIR/relay.log" 2>&1 &
RELAY_PID=$!

log "starting k6 load (duration=${LOAD_DURATION}s, vus=${LOAD_VUS}, hot=${LOAD_HOT})"
BASE_URL="$BASE_URL" API_KEY="$API_KEY" VUS="$LOAD_VUS" DURATION="${LOAD_DURATION}s" HOT="$LOAD_HOT" \
  k6 run "$ROOT_DIR/deploy/k6/wallet.js" > "$RESULTS_DIR/k6.log" 2>&1 &
K6_PID=$!

# --- chaos timeline ---------------------------------------------------------

CHAOS_START=$(date +%s)

log "t+5s: terminate Postgres backends (except our own)"
sleep 5
docker compose -f "$ROOT_DIR/docker-compose.yml" exec -T postgres psql -U postgres -d wallet -c \
  "SELECT pg_terminate_backend(pid) FROM pg_stat_activity
   WHERE datname='wallet' AND pid <> pg_backend_pid();" > "$RESULTS_DIR/pg-terminate.log" 2>&1 || true
log "pg terminations issued"

log "t+25s: stop Kafka for 30s"
sleep 20  # total t+25s
docker compose -f "$ROOT_DIR/docker-compose.yml" stop redpanda
log "Kafka stopped"
sleep 30
docker compose -f "$ROOT_DIR/docker-compose.yml" start redpanda
log "Kafka restarted"
wait_for_kafka_healthy 60

log "t+80s: kill relay"
sleep 25  # total ~t+80s from start of chaos
if kill -0 "$RELAY_PID" 2>/dev/null; then
  kill "$RELAY_PID"
  wait "$RELAY_PID" 2>/dev/null || true
  RELAY_PID=""
  log "relay killed"
else
  log "relay already exited"
fi

sleep 10
log "restarting relay"
DATABASE_URL="postgres://ledger_app:app_dev_pw@localhost:$POSTGRES_PORT/wallet?sslmode=disable" \
KAFKA_BROKERS="localhost:9092" \
KAFKA_TOPIC="ledger.events.v1" \
OUTBOX_BATCH_SIZE="50" \
OUTBOX_POLL_INTERVAL="1s" \
OUTBOX_MAX_BACKOFF="30s" \
  go run -C "$ROOT_DIR" ./cmd/relay >> "$RESULTS_DIR/relay.log" 2>&1 &
RELAY_PID=$!

# --- wait for k6 to finish --------------------------------------------------

log "waiting for k6 to finish (up to $((LOAD_DURATION + 60))s)"
wait "$K6_PID" 2>/dev/null || true
K6_PID=""

# --- post-chaos assertions --------------------------------------------------

log "post-chaos: wait for outbox drain"
wait_for_outbox_drain 180

log "post-chaos: check integrity"
check_integrity

log "post-chaos: check event consistency"
check_event_consistency

log "=== chaos test PASSED ==="
log "results written to $RESULTS_DIR"
