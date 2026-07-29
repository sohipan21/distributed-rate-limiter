#!/usr/bin/env bash
# Redis failover demo, and the measurement that matters.
#
# Sentinel keeps the service up when the master dies. That part is easy to show.
# The part worth measuring is what it costs: redis replicates asynchronously, so
# a promotion can lose the most recent counter writes and briefly let requests
# through that should have been denied. This spends a bucket, kills the master,
# and counts the excess.
#
#   ./demo/failover.sh        (or: make failover)
set -euo pipefail
cd "$(dirname "$0")/.."

COMPOSE="docker compose -f docker-compose.ha.yml"
BASE=${BASE_URL:-http://localhost:8080}
KEY=${API_KEY:-k_failover}
LIMIT=${LIMIT:-100}   # must match demo/config.failover.yaml
PROBE=${PROBE:-60}    # requests sent after the promotion

# fire n requests, echo how many were allowed
send() {
  local n=$1 allowed=0 code
  for _ in $(seq 1 "$n"); do
    code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/check" \
      -H 'Content-Type: application/json' -H "X-API-Key: $KEY" \
      -d '{"endpoint":"/download"}' || echo 000)
    [ "$code" = "200" ] && allowed=$((allowed + 1))
  done
  echo "$allowed"
}

master_addr() {
  $COMPOSE exec -T sentinel1 redis-cli -p 26379 \
    sentinel get-master-addr-by-name mymaster 2>/dev/null | head -1 | tr -d '\r'
}

# which container is master right now. after a previous failover the names no
# longer imply the roles — redis-replica may well be the master — so ask rather
# than assume, or the script kills the wrong box and proves nothing.
master_container() {
  local c
  for c in redis-master redis-replica; do
    if $COMPOSE exec -T "$c" redis-cli INFO replication 2>/dev/null \
        | tr -d '\r' | grep -q '^role:master'; then
      echo "$c"
      return 0
    fi
  done
  return 1
}

echo "== bringing up the HA stack (master + replica + 3 sentinels + 3 nodes) =="
# from scratch: a stack left over from an earlier run has the roles swapped and
# sentinel still remembers the old promotion
$COMPOSE down --remove-orphans >/dev/null 2>&1 || true
$COMPOSE up -d --build --wait

primary=$(master_container) || { echo "FAIL: no redis instance reports role:master"; exit 1; }
$COMPOSE exec -T "$primary" redis-cli FLUSHALL >/dev/null
sleep 1

before=$(master_addr)
echo "sentinel reports master: $before (container: $primary)"
echo "limit is $LIMIT per hour, so the bucket barely refills during the test."

echo
echo "== act 1: spend the whole bucket =="
spent=$(send $((LIMIT + 20)))
echo "sent $((LIMIT + 20)), allowed $spent (expected $LIMIT)"
extra=$(send 10)
echo "10 more: allowed $extra (expected 0 — bucket is empty)"

echo
echo "== act 2: kill the master, mid-write =="
# killing while writes are in flight is the case that can actually lose state:
# replication is asynchronous, so whatever the master acknowledged in the last
# few milliseconds may never have reached the replica. killing an idle master
# proves much less.
( send 200 > /tmp/failover-inflight.txt 2>/dev/null || true ) &
writer_pid=$!
sleep 0.5
$COMPOSE kill "$primary" >/dev/null
kill_ts=$(date +%s)
echo "master killed with requests in flight, waiting for promotion..."
wait "$writer_pid" 2>/dev/null || true
inflight=$(cat /tmp/failover-inflight.txt 2>/dev/null || echo 0)

promoted=""
for _ in $(seq 1 60); do
  now=$(master_addr)
  if [ -n "$now" ] && [ "$now" != "$before" ]; then
    promoted=$now
    break
  fi
  sleep 0.5
done
promote_secs=$(( $(date +%s) - kill_ts ))

if [ -z "$promoted" ]; then
  echo "FAIL: sentinel did not promote a new master within 30s"
  exit 1
fi
echo "promoted to $promoted after ~${promote_secs}s"

echo
echo "== act 3: is the limit still enforced? =="
# the client needs a moment to notice the new master through sentinel
sleep 2
leaked=$(send "$PROBE")
echo "sent $PROBE against an already-spent bucket, allowed $leaked"

echo
echo "== result =="
echo "promotion took ~${promote_secs}s; the service answered throughout."
echo
echo "allowed during the outage window: $inflight (of 200 sent across the kill)"
echo "  these are the fail-open policy doing its job, not lost state: with"
echo "  -degrade open an unreachable redis means requests pass. run the stack"
echo "  with -degrade closed and this number goes to zero, at the cost of"
echo "  denying real traffic while redis is gone."
echo
echo "allowed after promotion, against a bucket already spent: $leaked"
if [ "$leaked" -eq 0 ]; then
  echo "  no counter state lost — the replica had the writes that spent the bucket."
else
  echo "  $leaked requests got through that should have been denied. async"
  echo "  replication had not copied those writes when the master died, so the"
  echo "  promoted replica started from a staler count."
fi
echo
echo "see docs/06-failover.md for what this costs and when to care."

echo
echo "stack is still up. 'docker compose -f docker-compose.ha.yml down' to stop it."
