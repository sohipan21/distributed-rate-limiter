# Load testing

k6 harness for the `/check` endpoint. Traffic mixes ~100 identities across
free/paid tiers and two endpoints, so some keys stay under their limit and
hot ones blow through it — a 429 counts as success (that's the limiter
working), only unexpected statuses fail the run.

## Running

```
make up                                  # redis
go run ./cmd/server -redis localhost:6379
make loadtest                            # defaults: 300 rps for 30s
RATE=1000 DURATION=60s make loadtest     # or crank it
```

## Baseline (day 9)

300 rps for 30s, 9000 requests, zero errors. Server, redis, and k6 all on
one M2 Pro over localhost — absolute numbers are optimistic, the
memory-vs-redis comparison is the useful part.

| backend | p50 | p90 | p99 | max | allowed / denied |
|---------|-----:|-----:|-----:|------:|------------------|
| memory | 384µs | 716µs | 1.66ms | 5.01ms | 3665 / 5335 |
| redis | 1.12ms | 2.02ms | 3.65ms | 24.39ms | 3800 / 5200 |

Redis costs roughly +0.7ms at the median — one local round trip plus the
EVALSHA execution. That's the price of counters that hold across nodes,
and it's the number to watch when the multi-node compose setup lands in
week 3.

Raw k6 output: `results/baseline-memory.txt`, `results/baseline-redis.txt`.
