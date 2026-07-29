#!/usr/bin/env python3
"""Plot the saturation sweep from runs.csv: throughput and p99 against offered rate.

    python3 scripts/plot_saturation.py [results-dir]

Knee rules match summarize_saturation.sh exactly — latency knee is the first
rate whose median p99 crosses the budget, throughput knee the first delivering
under 95% of offered, and rates where any run exhausted k6's VU pool are
excluded from both.
"""
import csv
import pathlib
import statistics
import sys

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt  # noqa: E402

P99_BUDGET_MS = 50

d = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else "loadtest/results/saturation")
csv_path = d / "runs.csv"
if not csv_path.exists():
    sys.exit(f"no runs.csv in {d}")

by_rate = {}
for row in csv.DictReader(csv_path.open()):
    by_rate.setdefault(int(row["offered_rps"]), []).append(row)

steps = []
for rate in sorted(by_rate):
    runs = by_rate[rate]
    p99s = [float(r["p99_ms"]) for r in runs]
    # any run that hit the VU ceiling measured k6, not the service
    generator_bound = any(
        int(r["max_vus_configured"]) > 0
        and int(r["vus_max"]) >= int(r["max_vus_configured"]) * 0.99
        for r in runs
    )
    if generator_bound:
        continue
    steps.append(
        {
            "offered": rate,
            "achieved": statistics.median(float(r["achieved_rps"]) for r in runs),
            "p99": statistics.median(p99s),
            "p99_lo": min(p99s),
            "p99_hi": max(p99s),
        }
    )

if not steps:
    sys.exit("every rate was generator-bound; nothing to plot")

offered = [s["offered"] for s in steps]
achieved = [s["achieved"] for s in steps]
p99 = [s["p99"] for s in steps]
err_lo = [s["p99"] - s["p99_lo"] for s in steps]
err_hi = [s["p99_hi"] - s["p99"] for s in steps]

lat_knee = next((s for s in steps if s["p99"] > P99_BUDGET_MS), None)
thr_knee = next((s for s in steps if s["achieved"] < s["offered"] * 0.95), None)

fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(8, 7), sharex=True)

ax1.plot(offered, offered, "--", color="0.7", label="offered (ideal)")
ax1.plot(offered, achieved, "o-", color="#1f77b4", label="achieved")
ax1.set_ylabel("throughput (req/s)")
ax1.grid(alpha=0.3)

ax2.errorbar(
    offered, p99, yerr=[err_lo, err_hi], fmt="o-", color="#1f77b4",
    capsize=3, elinewidth=1, label="p99 (median, min–max of 3 runs)",
)
ax2.set_yscale("log")
ax2.set_ylabel("p99 latency (ms, log)")
ax2.set_xlabel("offered rate (req/s)")
ax2.grid(alpha=0.3, which="both")
ax2.axhline(P99_BUDGET_MS, color="0.75", linestyle="--", linewidth=1)

for knee, color, label in (
    (lat_knee, "#ff7f0e", f"latency knee (p99 > {P99_BUDGET_MS}ms)"),
    (thr_knee, "#d62728", "throughput knee (<95% delivered)"),
):
    if not knee:
        continue
    for ax in (ax1, ax2):
        ax.axvline(
            knee["offered"], color=color, linestyle=":", linewidth=1.4,
            label=f"{label} @ {knee['offered']:,}",
        )

ax1.legend(fontsize=8)
ax2.legend(fontsize=8)
ax1.set_title("Rate limiter saturation (3 nodes + nginx + 1 redis, one laptop)")
fig.tight_layout()

out = d / "saturation.png"
fig.savefig(out, dpi=140)
print(f"wrote {out}")
