#!/usr/bin/env bash
# Turn runs.csv into the markdown curve that goes in the README. Every rate is
# measured several times; this reports the median of each metric plus the p99
# spread, because near the knee a single run swings by 3x.
#
# Two knees, because they're different failures and they don't arrive together:
#   latency knee    — first rate whose median p99 crosses P99_BUDGET_MS. still
#                     keeping up on throughput, but the tail has gone.
#   throughput knee — first rate delivering under 95% of offered. the ceiling.
#
# Rates where any run exhausted k6's VU pool are excluded from both: those rows
# measure the generator, not the service.
#
#   ./scripts/summarize_saturation.sh [results-dir]
set -euo pipefail

DIR=${1:-loadtest/results/saturation}
CSV="$DIR/runs.csv"
[ -f "$CSV" ] || { echo "no runs.csv in $DIR"; exit 1; }

P99_BUDGET_MS=${P99_BUDGET_MS:-50}

awk -F, -v budget="$P99_BUDGET_MS" '
function median(arr, n,   i, j, t) {
  for (i = 2; i <= n; i++) { t = arr[i]; for (j = i-1; j >= 1 && arr[j] > t; j--) arr[j+1] = arr[j]; arr[j+1] = t }
  return (n % 2) ? arr[int((n+1)/2)] : (arr[n/2] + arr[n/2+1]) / 2
}
function r2(x) { return int(x * 100 + 0.5) / 100 }

NR == 1 { for (i = 1; i <= NF; i++) col[$i] = i; next }
{
  rate = $col["offered_rps"]
  if (!(rate in seen)) { rates[++nr] = rate; seen[rate] = 1 }
  n = ++cnt[rate]
  ach[rate, n] = $col["achieved_rps"]; p50[rate, n] = $col["p50_ms"]
  p99[rate, n] = $col["p99_ms"];       err[rate, n] = $col["error_rate"]
  alw[rate, n] = $col["allowed"];      den[rate, n] = $col["denied"]
  # a rate is generator-bound if ANY of its runs hit the VU ceiling
  if ($col["max_vus_configured"] > 0 && $col["vus_max"] >= $col["max_vus_configured"] * 0.99) gen[rate] = 1
}
END {
  n = asort_rates(rates, nr)
  print "| offered | achieved | p50 | p99 (median) | p99 range | errors | allowed/denied |"
  print "|--------:|---------:|----:|-------------:|----------:|-------:|----------------|"
  for (i = 1; i <= nr; i++) {
    rate = rates[i]; c = cnt[rate]
    for (k = 1; k <= c; k++) { a[k] = ach[rate, k]; b[k] = p50[rate, k]; d[k] = p99[rate, k]; e[k] = err[rate, k]; f[k] = alw[rate, k]; g[k] = den[rate, k] }
    ma = median(a, c); mb = median(b, c); md = median(d, c); me = median(e, c); mf = median(f, c); mg = median(g, c)
    lo = hi = p99[rate, 1]
    for (k = 1; k <= c; k++) { if (p99[rate, k] < lo) lo = p99[rate, k]; if (p99[rate, k] > hi) hi = p99[rate, k] }
    printf "| %d | %d | %sms | %sms | %s–%sms | %s%% | %d/%d |%s\n", rate, int(ma), r2(mb), r2(md), r2(lo), r2(hi), r2(me * 100), int(mf), int(mg), (rate in gen) ? "  <-- generator hit its VU cap; not a service measurement" : ""
    if (!(rate in gen)) {
      if (!latk && md > budget) { latk = rate; latv = md; latlo = lo; lathi = hi; lata = ma }
      if (!thrk && ma < rate * 0.95) { thrk = rate; thra = ma; thrv = md; thre = me }
    }
    reps = c
  }
  printf "\n%s\n", (reps == 1) ? "Single run per rate — near the knee that is a coin flip; raise REPS." : sprintf("Medians of %d runs per rate.", reps)
  if (latk) printf "Latency knee: %d offered — median p99 %sms (range %s–%sms), still delivering %d of %d.\n", latk, r2(latv), r2(latlo), r2(lathi), int(lata), latk
  else      printf "No latency knee: median p99 stayed under %dms at every rate.\n", budget
  if (thrk) printf "Throughput knee: %d offered — delivered %d (%s%% of offered), median p99 %sms, errors %s%%.\n", thrk, int(thra), r2(thra / thrk * 100), r2(thrv), r2(thre * 100)
  else      print  "No throughput knee: every rate delivered >=95% of offered. Raise RATES."
}
function asort_rates(arr, n,   i, j, t) {
  for (i = 2; i <= n; i++) { t = arr[i]; for (j = i-1; j >= 1 && arr[j]+0 > t+0; j--) arr[j+1] = arr[j]; arr[j+1] = t }
  return n
}
' "$CSV"
