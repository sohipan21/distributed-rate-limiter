# Atomicity and the clock

The first Redis-backed store did the obvious thing: read the state, do the
token math in Go, write it back. That version is kept in the repo
(`internal/store/naive.go`) because it demonstrates the core bug nicely.

## The race

Read-modify-write is two round trips with a gap in the middle:

```
node A                      node B                     redis
------                      ------                     -----
GET k        ------------------------------------>     tokens = 1
                            GET k        -------->     tokens = 1
compute: 1 >= 1, allow
                            compute: 1 >= 1, allow
SET k tokens=0    -------------------------------->    tokens = 0
                            SET k tokens=0    ---->    tokens = 0
```

Both nodes saw one token, both spent it. Writes in the gap get overwritten —
lost updates.

It's worse than a small leak. 50 goroutines pushing 500 requests at a limit
of 100, and the naive store allowed **all 500** — with enough concurrent
readers the counter never visibly depletes. Captured in
`TestNaiveBucketOvercountsUnderConcurrency`:

```
round 1: naive store allowed 500 of limit 100 (overcount 400, 500 requests total)
```

## The fix

Redis runs commands one at a time, and a Lua script counts as one command.
Both algorithms live as scripts now (`internal/store/lua/`), so the whole
check-and-update is serialized by Redis itself — no locks, no CAS loop, the
interleaving above simply can't happen. Scripts run via `EVALSHA`, so steady
state is still one round trip.

Same 500-request hammer, atomic stores: exactly 100 allowed, both
algorithms, every run. The before/after in one command:

```
make up && go test -v -run 'Overcounts|ExactUnder' ./internal/store/
```

## The clock

The naive store also trusted the node's own clock for refill math. With NTP
drift between nodes, each one computes different refill amounts — the
effective limit depends on which node the load balancer picks. So the
scripts ask Redis for the time (`redis.call('TIME')`) as their first step.
One clock, owned by the process that owns the data; node clocks are never
consulted.

All time math is integer microseconds, which fits a double exactly and
avoids Lua's habit of turning big numbers into `1.7834e+15` when they cross
the Redis boundary as strings.

## Limits of the approach

Atomicity makes the counting correct across any number of nodes. It does
nothing for availability — every decision now depends on one Redis being up.
Fail open or fail closed is week 3's problem and gets its own doc.

Memory stays bounded: both key types carry TTLs (a full refill horizon for
the bucket, one window for the log), so idle keys expire to nothing. Tested
in `internal/store/ttl_edge_test.go`.
