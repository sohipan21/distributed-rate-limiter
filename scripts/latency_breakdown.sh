#!/usr/bin/env bash
# Split the request latency into the four places it's actually spent, at a
# given offered rate.
#
#   lua            redis INFO commandstats, usec_per_call for evalsha
#   network+pool   client-side redis timing minus lua — round trip, plus any
#                  wait for a free connection in the pool
#   handler+policy whole-decision timing minus client-side redis timing
#   proxy+client   what k6 saw minus the whole decision — nginx, the go http
#                  stack, and the wire
#
# The first three come from counters the service already exports; only the lua
# number needs redis itself. Deltas across the run, so a warm process doesn't
# skew it.
#
# Output is scratch and gitignored: the numbers worth keeping live in
# docs/tradeoffs.md, and the lua figure cross-checks against the
# evalsha_usec_per_call column in loadtest/results/saturation/runs.csv.
#
#   ./scripts/latency_breakdown.sh [rate] [duration]
set -euo pipefail
cd "$(dirname "$0")/.."

RATE=${1:-${RATE:-10000}}
DURATION=${2:-${DURATION:-20s}}
NODES=${NODES:-"8081 8082 8083"}
OUTDIR=${OUTDIR:-loadtest/results/latency}

command -v k6 >/dev/null || { echo "k6 not installed"; exit 1; }
command -v jq >/dev/null || { echo "jq not installed"; exit 1; }
mkdir -p "$OUTDIR"

ulimit -n 65535 2>/dev/null || true

# sum a histogram's _sum and _count across every label combination and every
# node, so the average is over the whole cluster
scrape() {
  local metric=$1 field=$2 total=0
  for p in $NODES; do
    local v
    # %.6f throughout: these counters run to ~1e5 seconds while the delta over
    # one run is single-digit seconds, and awk's default %.6g output would
    # round the fraction away and report a fraction of the real number
    v=$(curl -s "http://localhost:${p}/metrics" \
        | awk -v m="${metric}_${field}" \
              '$1 ~ "^"m"([{]|$)" {s+=$NF} END {printf "%.6f\n", s+0}')
    total=$(awk -v a="$total" -v b="$v" 'BEGIN {printf "%.6f\n", a+b}')
  done
  echo "$total"
}

redis_evalsha() {
  docker compose exec -T redis redis-cli INFO commandstats 2>/dev/null \
    | tr -d '\r' | awk -F'[:,=]' '/^cmdstat_evalsha:/ {print $3, $5}'
}

echo "== warming up =="
docker compose up -d --wait >/dev/null
k6 run -e RATE=2000 -e DURATION=5s loadtest/check.js >/dev/null 2>&1 || true

echo "== measuring at ${RATE} rps for ${DURATION} =="
dec_sum0=$(scrape ratelimiter_decision_duration_seconds sum)
dec_cnt0=$(scrape ratelimiter_decision_duration_seconds count)
red_sum0=$(scrape ratelimiter_redis_duration_seconds sum)
red_cnt0=$(scrape ratelimiter_redis_duration_seconds count)
read -r ev_calls0 ev_usec0 <<<"$(redis_evalsha)"
: "${ev_calls0:=0}" "${ev_usec0:=0}"

# k6's own summary is scratch — every figure it contributes ends up in the
# breakdown table below, so it doesn't need to live in the repo
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

k6 run -e RATE="$RATE" -e DURATION="$DURATION" \
  -e OUT_JSON="$tmp/k6.json" loadtest/check.js \
  >/dev/null 2>&1 || true

dec_sum1=$(scrape ratelimiter_decision_duration_seconds sum)
dec_cnt1=$(scrape ratelimiter_decision_duration_seconds count)
red_sum1=$(scrape ratelimiter_redis_duration_seconds sum)
red_cnt1=$(scrape ratelimiter_redis_duration_seconds count)
read -r ev_calls1 ev_usec1 <<<"$(redis_evalsha)"
: "${ev_calls1:=0}" "${ev_usec1:=0}"

k6_avg_ms=$(jq -r '.avg_ms | .*10000 | round / 10000' "$tmp/k6.json" 2>/dev/null || echo 0)
k6_achieved=$(jq -r '.achieved_rps|floor' "$tmp/k6.json" 2>/dev/null || echo 0)

# everything in milliseconds
read -r decision redis lua < <(awk -v ds0="$dec_sum0" -v ds1="$dec_sum1" \
    -v dc0="$dec_cnt0" -v dc1="$dec_cnt1" \
    -v rs0="$red_sum0" -v rs1="$red_sum1" \
    -v rc0="$red_cnt0" -v rc1="$red_cnt1" \
    -v ec0="$ev_calls0" -v ec1="$ev_calls1" \
    -v eu0="$ev_usec0" -v eu1="$ev_usec1" \
  'BEGIN {
     dc = dc1 - dc0; rc = rc1 - rc0; ec = ec1 - ec0;
     printf "%.4f %.4f %.4f\n",
       (dc > 0 ? (ds1 - ds0) / dc * 1000 : 0),
       (rc > 0 ? (rs1 - rs0) / rc * 1000 : 0),
       (ec > 0 ? (eu1 - eu0) / ec / 1000 : 0);
   }')

{
  echo "# Where the latency goes"
  echo
  echo "Offered ${RATE} rps, achieved ${k6_achieved}, ${DURATION}. Means over the run."
  echo
  printf '| layer | mean ms | share | source |\n'
  printf '|---|--------:|------:|---|\n'
  awk -v d="$decision" -v r="$redis" -v l="$lua" -v k="$k6_avg_ms" 'BEGIN {
    proxy = k - d; if (proxy < 0) proxy = 0;
    net   = r - l; if (net < 0) net = 0;
    hand  = d - r; if (hand < 0) hand = 0;
    total = l + net + hand + proxy;
    if (total <= 0) total = 1;
    printf "| lua inside redis | %.3f | %.0f%% | measured (commandstats) |\n",        l,     100*l/total;
    printf "| redis round trip + pool wait | %.3f | %.0f%% | derived (redis call − lua) |\n", net, 100*net/total;
    printf "| handler + policy | %.3f | %.0f%% | derived (decision − redis call) |\n",  hand,  100*hand/total;
    printf "| nginx + go http + wire | %.3f | %.0f%% | **residual** (k6 − decision) |\n", proxy, 100*proxy/total;
    printf "| total | %.3f | | = k6 mean by construction |\n", total;
  }'
  echo
  echo "Directly measured, independently of each other:"
  echo "- whole decision, in-process: ${decision} ms"
  echo "- redis call, client side: ${redis} ms"
  echo "- lua execution, server side: ${lua} ms"
  echo "- end to end, at the client: ${k6_avg_ms} ms"
  echo
  echo "The last row is a residual, not a measurement — it absorbs everything"
  echo "between k6 and the handler, including any error in the other three. The"
  echo "total matching the k6 mean is arithmetic, not corroboration. What does"
  echo "corroborate: the lua figure here should match the evalsha_usec_per_call"
  echo "column in loadtest/results/saturation/runs.csv, and the decision figure"
  echo "should exceed the redis figure by the handler's own work."
} | tee "$OUTDIR/breakdown-${RATE}.md"
