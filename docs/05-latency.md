# Where the latency goes

A p99 number on its own doesn't tell you what to fix. This splits a `/check`
into the four places the time is actually spent, at light load and at the knee.

Reproduce with `./scripts/latency_breakdown.sh <rate> <duration>`; raw output is
in [`latency/`](latency/).

## The split

| layer | 2,000 rps | 12,000 rps | growth | source |
|---|--------:|---------:|-------:|---|
| lua inside redis | 0.021ms | 0.025ms | 1.2x | measured, `INFO commandstats` |
| redis round trip + pool wait | 0.131ms | 4.211ms | **32x** | derived (redis call − lua) |
| handler + policy | 0.001ms | 0.002ms | 2x | derived (decision − redis call) |
| nginx + go http + wire | 0.689ms | 6.357ms | **9x** | residual (k6 − decision) |
| end to end | 0.842ms | 10.595ms | 13x | measured at the client |

Three of these are measured; the last is a residual and absorbs any error in the
other three, so the total adding up to the client figure is arithmetic rather
than corroboration. What does corroborate: the lua figure matches
`usec_per_call` for `evalsha` in the saturation runs (~22–27µs across every
rate), and it was derived from a completely separate source.

## What it says

**The rate limiting is not the expensive part.** Deciding whether to allow a
request — refill the bucket, compare, write back, set the TTL, all inside one
Lua script — costs about 25µs and barely moves between light load and the knee.
The Go handler and policy resolution together cost about 2µs. Together they are
under 0.3% of what the caller waits for.

**Everything expensive is transport.** At 12k rps the client-side Redis call
takes 4.2ms while the script it's waiting on runs in 0.025ms: 99.4% of that call
is round trip and waiting for a free connection in the pool, not work. The proxy
hop is larger still.

**What grows under load is queueing, not computation.** From 2k to 12k the Lua
execution grows 1.2x while the Redis round trip grows 32x and the proxy 9x.
That's the signature of queueing rather than saturation of the work itself, and
it lines up with the
[bottleneck attribution](../loadtest/results/saturation/attribution/README.md):
one node hit directly sustains ~15k rps where the proxied cluster manages ~13.8k.

## What that implies for making it faster

In priority order, based on the numbers above rather than on instinct:

1. **Fewer round trips per decision.** The single biggest lever is not making
   the script faster — it's already 25µs — but not paying 4ms to reach it.
   Pipelining independent checks, or a short-lived local cache for keys that are
   already known to be over their limit, would cut the dominant term.
2. **A bigger connection pool, or measuring that it isn't the wait.** `-redis-pool`
   exists for this; the pool-wait component is currently folded in with the round
   trip and would need `PoolStats` to separate.
3. **Proxy work, not limiter work.** See the attribution doc — this is the
   larger of the two transport terms.

Nothing here suggests touching the Lua or the Go hot path. That's the point of
measuring first.
