# Load testing

k6 harnesses for the `/check` endpoint. Traffic mixes ~100 identities across
free/paid tiers and two endpoints, so some keys stay under their limit and
hot ones blow through it — a 429 counts as success (that's the limiter
working), only unexpected statuses fail the run.

## Running one rate

```
make up                                  # redis + 3 nodes + nginx
make loadtest                            # defaults: 300 rps for 30s
RATE=5000 DURATION=60s make loadtest     # or crank it
```

## Finding the ceiling

`make saturate` steps through a range of offered rates (3 runs each, reported
as medians) and reports the knee: the rate where p99 breaks or throughput
stops keeping up. Results and machine specs land in
[`results/saturation/`](results/saturation), including the bottleneck
attribution in [`results/saturation/attribution`](results/saturation/attribution)
that separates the generator, Redis, and the proxy as candidate causes.
Numbers and the full writeup are in the main [README](../README.md#results).

`make breakdown` splits a single request into where its latency goes (Lua,
Redis round trip, handler, proxy) at light load and at the knee. Writeup in
[docs/05-latency.md](../docs/05-latency.md).

## Baseline (single node, memory vs redis)

300 rps for 30s, 9000 requests, zero errors. Server, redis, and k6 all on one
M2 Pro over localhost — absolute numbers are optimistic, the memory-vs-redis
comparison is the useful part.

| backend | p50 | p90 | p99 | max | allowed / denied |
|---------|-----:|-----:|-----:|------:|------------------|
| memory | 384µs | 716µs | 1.66ms | 5.01ms | 3665 / 5335 |
| redis | 1.12ms | 2.02ms | 3.65ms | 24.39ms | 3800 / 5200 |

Redis costs roughly +0.7ms at the median — one local round trip plus the
EVALSHA execution. That's the price of counters that hold across nodes. The
saturation sweep above measures the multi-node cluster this baseline predates.

Raw k6 output: `results/baseline-memory.txt`, `results/baseline-redis.txt`.
