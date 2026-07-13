#!/usr/bin/env bash
# Kill-redis degradation demo. Brings up the multi-node stack, shows the
# limit being enforced, kills redis mid-traffic to show the cluster fail
# open (keep serving instead of crashing), then recovers when redis returns.
#
# Also a smoke test: it asserts each act behaved and exits non-zero if not.
#
#   ./demo/kill-redis.sh        (or: make demo)
set -euo pipefail
cd "$(dirname "$0")/.."

BASE=${BASE_URL:-http://localhost:8080}
PAUSE=${PAUSE:-0.1}
RUN=$(date +%s) # suffix so re-runs don't inherit a counter within the window

LAST_429=0

# fire N requests for one identity through nginx, print statuses inline,
# stash the 429 count in LAST_429
send() {
  local id=$1 n=$2 code count=0
  for _ in $(seq 1 "$n"); do
    code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/check" \
      -H 'Content-Type: application/json' \
      -d "{\"identity\":\"$id\",\"tier\":\"free\",\"endpoint\":\"/download\"}")
    printf '%s ' "$code"
    [ "$code" = "429" ] && count=$((count + 1))
    sleep "$PAUSE"
  done
  printf '\n'
  LAST_429=$count
}

echo "== bringing up the stack (redis + 3 nodes + nginx) =="
docker compose up -d --build --wait
docker compose exec -T redis redis-cli FLUSHALL >/dev/null
echo "ready. free tier is 10 requests/min, enforced cluster-wide."

echo
echo "== act 1: redis up, the limit is enforced =="
echo "13 requests for one identity -- expect 10x 200 then 429s:"
send "alice-$RUN" 13
act1=$LAST_429

echo
echo "== act 2: kill redis mid-flight =="
docker compose stop redis >/dev/null
echo "redis is down. 13 requests for a fresh identity -- expect all 200:"
send "bob-$RUN" 13
act2=$LAST_429
echo "node log:"
docker compose logs --since 30s node1 node2 node3 2>&1 | grep 'degraded:' | tail -1 || true

echo
echo "== act 3: bring redis back =="
docker compose start redis >/dev/null
until docker compose exec -T redis redis-cli ping 2>/dev/null | grep -q PONG; do sleep 0.5; done
# nudge every node's breaker past its cooldown so all have recovered
for _ in $(seq 1 12); do
  curl -s -o /dev/null -X POST "$BASE/check" \
    -d "{\"identity\":\"warmup-$RUN\",\"tier\":\"free\",\"endpoint\":\"/download\"}" || true
  sleep 0.3
done
echo "node log:"
docker compose logs --since 15s node1 node2 node3 2>&1 | grep 'recovered:' | tail -1 || true
docker compose exec -T redis redis-cli FLUSHALL >/dev/null
echo "enforcement resumed. 13 requests for another identity -- expect 10x 200 then 429s:"
send "carol-$RUN" 13
act3=$LAST_429

echo
fail=0
[ "$act1" -ge 1 ] || { echo "FAIL: act 1 never returned 429 (enforcement broken)"; fail=1; }
[ "$act2" -eq 0 ] || { echo "FAIL: act 2 returned a 429 (did not fail open)"; fail=1; }
[ "$act3" -ge 1 ] || { echo "FAIL: act 3 never returned 429 (enforcement did not resume)"; fail=1; }
if [ "$fail" -ne 0 ]; then
  exit 1
fi

echo "PASS: enforced ($act1 denied) -> degraded (0 denied) -> recovered ($act3 denied)"
echo "stack is still up; run 'make down' to stop it."
