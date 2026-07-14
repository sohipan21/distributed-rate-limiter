# Load test report

Numbers from k6 driving the full compose cluster (nginx in front of three
nodes sharing one redis), everything on one M2 Pro. These are localhost
numbers, so treat them as a relative picture, not production figures. Traffic
is the mix from `loadtest/check.js`: ~100 identities across two tiers and two
endpoints, so some callers stay under their limit and hot ones get throttled.

## Steady load

Three 60-second runs through nginx. Raw k6 output is in `loadtest/results/`.

| offered | achieved | p50 | p95 | p99 | allowed / denied |
|---------|----------|------|------|------|------------------|
| 300 rps | 300 rps | 1.31ms | 2.97ms | 4.82ms | 5075 / 12903 |
| 1000 rps | 1000 rps | 0.69ms | 0.95ms | 1.39ms | 5223 / 54777 |
| 2000 rps | 2000 rps | 0.58ms | 0.78ms | 1.34ms | 5253 / 114749 |

The cluster held every offered rate with no 5xx and p99 under 5ms. The 300 rps
row is actually the slowest because it ran first on a cold stack; once warm,
1000 and 2000 rps sit lower. No knee showed up at 2000 rps on this hardware.

One thing the table makes clear: allowed requests stay flat near 5200 no matter
how hard I push, while denied grows with the load. That is the point. The
limits are per minute, so past a certain rate the extra traffic just becomes
429s and the allowed rate stays put.

![ramp](img/loadtest-ramp.png)

## Redis dies mid-test

A 90-second run at 500 rps with redis stopped at t+30s and started again at
t+60s.

| p50 | p95 | p99 | max | allowed / denied | failed |
|------|------|------|------|------------------|--------|
| 1.02ms | 2.13ms | 5.47ms | 733ms | 21859 / 23132 | 0 |

44,991 requests, zero failures. While redis was down the nodes failed open, so
requests that would have been 429s came back 200 instead. That is why allowed
jumps from ~87/s in the steady runs to ~243/s here. The 733ms max is the first
few requests per node that hit the redis timeout before the breaker tripped and
started skipping redis. The dashboard shows the whole arc: throughput steady,
the redis-status panel flips to degraded, then recovers.

![kill redis](img/loadtest-kill-redis.png)

## Accuracy

`./scripts/verify-multinode.sh` against the loaded cluster: one identity across
three nodes, exactly 10 allowed and 5 denied at a limit of 10. The exact count
under concurrency is pinned by the store tests (`TestAtomicBucketExactUnderConcurrency`
and friends), where the naive store lets 500 through and the atomic one holds
at 100.

## Limits

Single machine, synthetic traffic, one-minute policy windows. Good enough to
show the shape and prove the failure behavior, not a substitute for a real
staging run.
