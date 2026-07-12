#!/usr/bin/env bash
# proves the limit holds across nodes: 15 requests for ONE identity, spread
# over the three nodes directly. free tier is 10/min, so a shared counter
# allows exactly 10 and denies 5. per-node counters would allow all 15 —
# that's the failure this script exists to catch.
set -euo pipefail

NODES=(localhost:8081 localhost:8082 localhost:8083)
ID="crossnode-$(date +%s)" # fresh identity per run; redis remembers for a window
allowed=0
denied=0

for i in $(seq 0 14); do
  node=${NODES[$((i % 3))]}
  code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://$node/check" \
    -d "{\"identity\":\"$ID\",\"tier\":\"free\",\"endpoint\":\"/download\"}")
  case $code in
    200) allowed=$((allowed + 1)) ;;
    429) denied=$((denied + 1)) ;;
    *)
      echo "FAIL: unexpected status $code from $node"
      exit 1
      ;;
  esac
done

echo "one identity across 3 nodes: $allowed allowed, $denied denied (limit 10)"
if [ "$allowed" -ne 10 ] || [ "$denied" -ne 5 ]; then
  echo "FAIL: limit did not hold across nodes"
  exit 1
fi

# sanity: a fresh identity through the load balancer gets its own quota
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST http://localhost:8080/check \
  -d "{\"identity\":\"$ID-other\",\"tier\":\"free\",\"endpoint\":\"/download\"}")
if [ "$code" != "200" ]; then
  echo "FAIL: nginx path returned $code"
  exit 1
fi

echo "PASS: shared counter held across nodes"
