# Where the latency goes

A p99 number on its own doesn't tell you what to fix. This splits a `/check` into
the four places the time goes, at light load and at the knee.

Reproduce with `./scripts/latency_breakdown.sh <rate> <duration>`; output is in
[`latency/`](latency/).

## The split

| layer | 2,000 rps | 12,000 rps | growth | source |
|---|--------:|---------:|-------:|---|
| lua inside redis | 0.021ms | 0.025ms | 1.2x | measured, `INFO commandstats` |
| redis round trip + pool wait | 0.131ms | 4.211ms | 32x | derived (redis call − lua) |
| handler + policy | 0.001ms | 0.002ms | 2x | derived (decision − redis call) |
| nginx + go http + wire | 0.689ms | 6.357ms | 9x | residual (k6 − decision) |
| end to end | 0.842ms | 10.595ms | 13x | measured at the client |

Three of these are measured. The last is a residual, so it absorbs any error in
the other three and the total matching the client figure is arithmetic, not
confirmation. The one real cross-check is the lua figure: it comes from Redis's
own counters, and it matches the `evalsha_usec_per_call` column in the saturation
runs (~22–27µs at every rate).

## What it says

Deciding a request is cheap. Refill the bucket, compare, write back, set the
TTL: all one Lua script, about 25µs, and it barely moves between light load and
the knee. The handler and policy resolution add ~2µs. Together that's under 0.3%
of what the caller waits for.

The rest is transport. At 12k rps the client-side Redis call takes 4.2ms while
the script it's waiting on runs in 0.025ms, so 99.4% of that call is round trip
and waiting for a free connection. The proxy hop is bigger still.

What grows under load is queueing, not computation: from 2k to 12k the Lua
execution grows 1.2x while the round trip grows 32x and the proxy 9x. That lines
up with the [bottleneck attribution](../loadtest/results/saturation/attribution/README.md),
where one node hit directly sustains ~15k rps and the proxied cluster manages
~13.8k.

## What would make it faster

In priority order, from the numbers above:

1. Fewer round trips per decision. The lever isn't a faster script, it's already
   25µs. It's not paying 4ms to reach it. Pipelining independent checks, or a
   short-lived local cache for keys already known to be over their limit, would
   cut the dominant term.
2. A bigger pool, or ruling out the pool. `-redis-pool` exists for this, but the
   pool-wait component is folded in with the round trip and would need
   `PoolStats` to separate.
3. Proxy work rather than limiter work. See the attribution doc; it's the larger
   of the two transport terms.

None of this points at the Lua or the Go hot path.
