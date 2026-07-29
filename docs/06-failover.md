# Redis failover, and what it costs

The service nodes are stateless, so losing one costs nothing. Redis holds every
counter, so losing *it* used to take the whole thing down — the availability
story stopped at the tier that didn't have the state.

`docker-compose.ha.yml` puts a replica and three sentinels behind the master.
Nodes connect through the sentinels (`-redis-sentinel`), so when the master dies
they follow the promotion instead of pointing at a corpse.

```
node1 ─┐
node2 ─┼─> sentinel x3 ──> redis-master ──async──> redis-replica
node3 ─┘   (quorum 2)          │                        │
                               └────── promoted on ─────┘
                                       master failure
```

Reproduce with `make failover`.

## What the test does

Rate limiters are one of the few systems where you can measure correctness loss
directly: spend a bucket completely, kill the master, and count how many
requests get through that should have been denied.

The config for it (`demo/config.failover.yaml`) uses a limit of 100 per **hour**
so the bucket refills at 0.028 tokens/sec — a 20-second failover returns about
half a token. Anything above that is state the failover actually lost, not
normal refill.

The master is killed **with writes in flight**. Killing an idle master proves
much less: replication is asynchronous, so the writes at risk are exactly the
ones acknowledged in the last few milliseconds.

## Measured

| | |
|---|---|
| enforcement before failover | 100 of 120 allowed, then 0 of 10 — exact |
| promotion time | ~5s (`down-after-milliseconds 1000`, quorum 2 of 3) |
| requests served during the outage | all of them, none failed |
| allowed during the outage window | 4 of 200 |
| **allowed after promotion, bucket already spent** | **0** |

Two different numbers, and it matters which is which.

**The 4 are the fail-open policy working as designed.** With `-degrade open`,
an unreachable Redis means requests pass — availability chosen over the
guarantee, which is the right default for a limiter sitting in front of
everything else. Running with `-degrade closed` takes that to zero and denies
real traffic instead. That trade is covered in
[04-tradeoffs.md](04-tradeoffs.md).

**The 0 is the interesting one.** It says the replica had every write that spent
the bucket, so the promotion cost no correctness. That is the good case, not a
guarantee: master and replica are containers on one host here, so replication
lag is microseconds. Across availability zones, under a heavier write rate, or
with a slow replica, writes acknowledged by the master but not yet shipped would
be lost and the promoted replica would start from a staler count — over-admitting
by roughly (write rate × replication lag).

## What would make it a guarantee

Nothing here waits for replica acknowledgement. `WAIT 1 <ms>` after each write
would turn "probably replicated" into "replicated or the write reports failure",
at the cost of a second round trip on the hot path — which
[05-latency.md](05-latency.md) shows is already the dominant term, so it would
roughly double decision latency. For rate limiting that is a bad trade: briefly
over-admitting a handful of requests during a rare failover is cheaper than
paying a synchronous replication round trip on every request forever.

The honest summary: this setup removes Redis as a single point of failure and
costs a few seconds of fail-open plus a bounded, measurable amount of
over-admission. It does not make the counter durable, and it isn't trying to.
