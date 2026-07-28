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

# highest CPU any container matching $1 reached during the run
peak_cpu() {
  awk -v pat="$1" '$1 ~ pat { gsub(/%/, "", $2); if ($2+0 > max) max = $2+0 }
                   END { printf "%.1f", max+0 }' "$2" 2>/dev/null
}

# One row per run. Every number the README, curve.md and the attribution
# writeup quote is a column here — there is deliberately no per-run dump.
CSV="$OUTDIR/runs.csv"
echo "offered_rps,rep,achieved_rps,p50_ms,p99_ms,p999_ms,max_ms,error_rate,requests,dropped_iterations,vus_max,max_vus_configured,allowed,denied,evalsha_calls,evalsha_usec_per_call,nginx_cpu_pct,redis_cpu_pct,node_cpu_pct_max" > "$CSV"

echo "== sweeping: $RATES ($REPS reps each, ${DURATION} per rep) =="
for rate in $RATES; do
  echo
  echo "-- offered ${rate} rps --"

  for rep in $(seq 1 "$REPS"); do
    tmp=$(mktemp -d)

    docker compose exec -T redis redis-cli FLUSHALL >/dev/null
    docker compose exec -T redis redis-cli CONFIG RESETSTAT >/dev/null

    # sample container CPU through the run; the peak per tier is what says
    # which one saturated
    (
      while true; do
        docker stats --no-stream --format '{{.Name}} {{.CPUPerc}}' >> "$tmp/stats" 2>/dev/null || true
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
      -e OUT_JSON="$tmp/step.json" \
      loadtest/check.js > "$tmp/k6.log" 2>&1
    k6_exit=$?
    set -e

    kill "$stats_pid" 2>/dev/null || true
    wait "$stats_pid" 2>/dev/null || true

    # redis server-side timing: usec_per_call for evalsha is the lua execution
    docker compose exec -T redis redis-cli INFO commandstats > "$tmp/cs" 2>/dev/null || true

    if [ -f "$tmp/step.json" ]; then
      metrics=$(jq -r '[
        (.achieved_rps*10|round/10),
        (.p50_ms*100|round/100), (.p99_ms*100|round/100),
        (.p999_ms*100|round/100), (.max_ms*100|round/100),
        (.error_rate*100000|round/100000),
        .requests, .dropped_iterations, .vus_max, .max_vus_configured,
        .allowed, .denied
      ] | map(tostring) | join(",")' "$tmp/step.json")

      ev=$(awk -F'[:,=]' '/^cmdstat_evalsha:/ {print $3","$7; exit}' "$tmp/cs" 2>/dev/null)
      : "${ev:=,}"

      echo "${rate},${rep},${metrics},${ev},$(peak_cpu nginx "$tmp/stats"),$(peak_cpu redis "$tmp/stats"),$(peak_cpu node "$tmp/stats")" >> "$CSV"

      jq -r '"  rep '"$rep"': achieved \(.achieved_rps|floor) rps | p50 \(.p50_ms*100|round/100)ms | p99 \(.p99_ms*100|round/100)ms | errors \(.error_rate*10000|round/100)% | dropped \(.dropped_iterations|floor) | vus \(.vus_max)"' \
        "$tmp/step.json"
    fi

    # k6 exits 99 when a threshold is breached; that is a data point, not a failure
    [ "$k6_exit" -eq 0 ] || echo "    (k6 exit $k6_exit — threshold breached, expected past the knee)"

    # the per-run dumps are scratch: everything cited downstream is now a column
    # in runs.csv, and 80-odd raw INFO dumps in the repo help nobody
    rm -rf "$tmp"
  done
done

echo
echo "== curve =="
"$(dirname "$0")/summarize_saturation.sh" "$OUTDIR" | tee "$OUTDIR/curve.md"
echo
echo "wrote $OUTDIR"
