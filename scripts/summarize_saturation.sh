#!/usr/bin/env bash
# Turn the per-step JSON from saturate.sh into the markdown curve that goes in
# the README. Every rate is measured several times; this reports the median of
# each metric, plus the p99 spread, because near the knee a single run swings
# by 3x and one number would be a coin flip.
#
# Two knees, because they're different failures and they don't arrive together:
#   latency knee    — first rate whose median p99 crosses 50ms. still keeping
#                     up on throughput, but the tail has gone.
#   throughput knee — first rate delivering under 95% of offered. the ceiling.
#
#   ./scripts/summarize_saturation.sh [results-dir]
set -euo pipefail

DIR=${1:-loadtest/results/saturation}
[ -d "$DIR" ] || { echo "no such directory: $DIR"; exit 1; }

P99_BUDGET_MS=${P99_BUDGET_MS:-50}

jq -rs --argjson budget "$P99_BUDGET_MS" '
  def median(f):
    (map(f) | sort) as $s
    | ($s | length) as $n
    | if $n == 0 then 0
      elif $n % 2 == 1 then $s[($n - 1) / 2]
      else ($s[$n / 2 - 1] + $s[$n / 2]) / 2
      end;
  def r2: . * 100 | round / 100;

  group_by(.offered_rps)
  | map({
      offered_rps: .[0].offered_rps,
      reps: length,
      achieved_rps: median(.achieved_rps),
      p50_ms: median(.p50_ms),
      p99_ms: median(.p99_ms),
      p99_min: (map(.p99_ms) | min),
      p99_max: (map(.p99_ms) | max),
      max_ms: median(.max_ms),
      error_rate: median(.error_rate),
      dropped: median(.dropped_iterations),
      allowed: median(.allowed),
      denied: median(.denied),
      # generator-bound only when k6 exhausted its VU pool. dropped iterations
      # alone do not mean that: with VUs still free, drops are the service
      # replying slowly, which is a result rather than an artifact
      generator_bound: ((
        map(select((.max_vus_configured // 0) > 0
                   and .vus_max >= (.max_vus_configured * 0.99)))
        | length
      ) > 0),
    })
  | sort_by(.offered_rps)
  | (map(select(.generator_bound | not))) as $valid
  | ($valid | map(select(.p99_ms > $budget)) | first) as $lat
  | ($valid | map(select(.achieved_rps < (.offered_rps * 0.95))) | first) as $thr
  | "| offered | achieved | p50 | p99 (median) | p99 range | errors | allowed/denied |",
    "|--------:|---------:|----:|-------------:|----------:|-------:|----------------|",
    (.[] |
      "| \(.offered_rps) | \(.achieved_rps|floor)"
      + " | \(.p50_ms|r2)ms | \(.p99_ms|r2)ms"
      + " | \(.p99_min|r2)–\(.p99_max|r2)ms"
      + " | \(.error_rate*10000|round/100)%"
      + " | \(.allowed|floor)/\(.denied|floor) |"
      + (if .generator_bound then "  <-- generator hit its VU cap; not a service measurement" else "" end)
    ),
    "",
    (if .[0].reps == 1
     then "Single run per rate — near the knee that is a coin flip; raise REPS."
     else "Medians of \(.[0].reps) runs per rate." end),
    (if $lat == null
     then "No latency knee: median p99 stayed under \($budget)ms at every rate."
     else "Latency knee: \($lat.offered_rps) offered — median p99 \($lat.p99_ms|r2)ms"
          + " (range \($lat.p99_min|r2)–\($lat.p99_max|r2)ms),"
          + " still delivering \($lat.achieved_rps|floor) of \($lat.offered_rps)."
     end),
    (if $thr == null
     then "No throughput knee: every rate delivered >=95% of offered. Raise RATES."
     else "Throughput knee: \($thr.offered_rps) offered — delivered \($thr.achieved_rps|floor)"
          + " (\((($thr.achieved_rps / $thr.offered_rps) * 1000 | round) / 10)% of offered),"
          + " median p99 \($thr.p99_ms|r2)ms, errors \($thr.error_rate*10000|round/100)%."
     end)
' "$DIR"/step-*.json
