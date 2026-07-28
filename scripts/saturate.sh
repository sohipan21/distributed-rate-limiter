#!/usr/bin/env bash
# Saturation sweep: step the offered rate up until the service stops keeping
# up, and record enough per step to say WHY it stopped.
#
# Each step is its own k6 run at a fixed arrival rate, so every row gets clean
# percentiles instead of an average smeared across a ramp. Before each step
# redis stats are reset; after it they're captured, which gives the server-side
# lua timing the latency breakdown needs.
#
#   ./scripts/saturate.sh                          (or: make saturate)
#   RATES="1000 5000 20000" DURATION=60s ./scripts/saturate.sh
set -euo pipefail
cd "$(dirname "$0")/.."

BASE=${BASE_URL:-http://localhost:8080}
RATES=${RATES:-"1000 2000 5000 8000 10000 12000 15000 20000"}
DURATION=${DURATION:-20s}
KEYS=${KEYS:-100}
# near the knee a single run swings by 3x, so every rate is measured REPS
# times and the summary reports medians
REPS=${REPS:-3}
OUTDIR=${OUTDIR:-loadtest/results/saturation}

# macOS defaults to 256 open files, which binds long before the service does.
# raise it in this shell so the generator isn't what we end up measuring.
ulimit -n 65535 2>/dev/null || echo "warning: could not raise ulimit -n (now $(ulimit -n))"

command -v k6 >/dev/null || { echo "k6 not installed"; exit 1; }
command -v jq >/dev/null || { echo "jq not installed"; exit 1; }

mkdir -p "$OUTDIR"

echo "== bringing up the stack (redis + 3 nodes + nginx, no prometheus/grafana) =="
docker compose up -d --build --wait

# machine + stack provenance, so the numbers are reproducible later
{
  echo "date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "host: $(uname -mrs)"
  echo "cpu: $(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo unknown)"
  echo "physical cores: $(sysctl -n hw.physicalcpu 2>/dev/null || echo unknown)"
  echo "docker cpus: $(docker info --format '{{.NCPU}}')"
  echo "docker mem: $(docker info --format '{{.MemTotal}}')"
  echo "ulimit -n: $(ulimit -n)"
  echo "k6: $(k6 version | head -1)"
  echo "redis: $(docker compose exec -T redis redis-cli INFO server | grep redis_version | tr -d '\r')"
  echo "nodes: 3 behind nginx"
  echo "note: k6 runs on the same laptop as the stack — see README caveat"
} > "$OUTDIR/specs.txt"

echo "== sweeping: $RATES ($REPS reps each, ${DURATION} per rep) =="
for rate in $RATES; do
  echo
  echo "-- offered ${rate} rps --"

  for rep in $(seq 1 "$REPS"); do
    tag="${rate}-rep${rep}"

    docker compose exec -T redis redis-cli FLUSHALL >/dev/null
    docker compose exec -T redis redis-cli CONFIG RESETSTAT >/dev/null

    # sample container CPU through the run; the samples are what tell us which
    # tier saturated
    stats_file="$OUTDIR/stats-${tag}.txt"
    : > "$stats_file"
    (
      while true; do
        echo "--- $(date +%H:%M:%S)" >> "$stats_file"
        docker stats --no-stream --format '{{.Name}} {{.CPUPerc}} {{.MemUsage}}' >> "$stats_file" 2>/dev/null || true
        sleep 5
      done
    ) &
    stats_pid=$!

    set +e
    k6 run \
      -e BASE_URL="$BASE" \
      -e RATE="$rate" \
      -e DURATION="$DURATION" \
      -e KEYS="$KEYS" \
      -e OUT_JSON="$OUTDIR/step-${tag}.json" \
      loadtest/check.js > "$OUTDIR/.k6-raw.txt" 2>&1
    k6_exit=$?
    set -e

    # every metric lands in step-<tag>.json via handleSummary, so k6's stdout is
    # just the progress stream — megabytes of it on a failing step. Keep only a
    # tally of distinct errors, and only when there were any.
    if grep -qE '^(ERRO|WARN)' "$OUTDIR/.k6-raw.txt" 2>/dev/null; then
      {
        echo "distinct errors/warnings from k6 (count, message):"
        grep -hE '^(ERRO|WARN)' "$OUTDIR/.k6-raw.txt" \
          | sed 's/time="[^"]*" //' | sort | uniq -c | sort -rn | head -20
      } > "$OUTDIR/k6-errors-${tag}.txt"
    fi
    rm -f "$OUTDIR/.k6-raw.txt"

    kill "$stats_pid" 2>/dev/null || true
    wait "$stats_pid" 2>/dev/null || true

    # per-rep redis server-side timing (usec_per_call for evalsha == lua exec)
    docker compose exec -T redis redis-cli INFO commandstats > "$OUTDIR/redis-commandstats-${tag}.txt" 2>/dev/null || true
    docker compose exec -T redis redis-cli INFO clients >> "$OUTDIR/redis-commandstats-${tag}.txt" 2>/dev/null || true

    if [ -f "$OUTDIR/step-${tag}.json" ]; then
      jq -r '"  rep '"$rep"': achieved \(.achieved_rps|floor) rps | p50 \(.p50_ms*100|round/100)ms | p99 \(.p99_ms*100|round/100)ms | errors \(.error_rate*10000|round/100)% | dropped \(.dropped_iterations|floor) | vus \(.vus_max)"' \
        "$OUTDIR/step-${tag}.json"
    fi
    # k6 exits 99 when a threshold is breached; that is a data point, not a failure
    [ "$k6_exit" -eq 0 ] || echo "    (k6 exit $k6_exit — threshold breached, expected past the knee)"
  done
done

echo
echo "== curve =="
"$(dirname "$0")/summarize_saturation.sh" "$OUTDIR" | tee "$OUTDIR/curve.md"
echo
echo "wrote $OUTDIR"
