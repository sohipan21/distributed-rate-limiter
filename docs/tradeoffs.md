# Tradeoffs

Making a limit exact across several nodes means sharing state, and shared state
costs something. This is each cost, measured.

## Getting the count right

The obvious way to count is: read the current number, add one, write it back.
That breaks the moment two nodes do it at the same time. Both read "9 so far",
both decide the request is fine, both write "10". One request just got lost,
and the limit leaks. Under a real concurrent flood it leaks badly — the
concurrency test in `internal/store` pushes 500 requests through a limit of
100 with the naive version.

The fix is to make check-and-update a single step that nothing can interrupt.
Here that step is a Lua script executed inside Redis, which handles one script
at a time, so across every node the count can only move one request at a time.
The scripts also read Redis's own clock (`TIME`) instead of the node's, so a
window boundary is the same instant on every node even when their local clocks
drift.

## The cost: every node depends on Redis

One shared counter is what makes the limit exact, but it also means every node
now depends on Redis. So when Redis is unreachable, there's a choice with no
clean answer. You can keep the limits correct, or you can keep serving
traffic. You can't do both.

```
healthy:        node -> redis -> exact count
redis down:     node -> redis  x   ...now what?
                  allow it through, or reject it?
```

That's the whole tradeoff. The `-degrade` flag picks which side you land on.

It costs latency too. Same workload at 300 rps on one node, in-memory limiters
versus Redis:

| backend | p50 | p99 | max |
|---------|-----:|-----:|------:|
| memory | 384µs | 1.66ms | 5.01ms |
| redis | 1.12ms | 3.65ms | 24.39ms |

Roughly +0.7ms at the median, for one local round trip plus the script. That's
the price of counters that hold across nodes. What happens to it under load is
further down. Raw k6 output for both rows is in
[`loadtest/results/`](../loadtest/results).

## Why I default to fail-open

Allow requests through when Redis is down. The reasoning is simple. A rate
limiter exists to protect the service behind it. If the limiter's own outage
takes that service down, it has done the opposite of its job. For ordinary
throttling like free vs paid tiers or stopping a noisy client, a short window
where limits aren't enforced is annoying but survivable. A full outage is not.

So the default keeps the service up. You lose enforcement for a few seconds
until Redis comes back, and `make demo` shows exactly that. The circuit
breaker keeps that window cheap. It stops hammering a dead Redis after a few
failures instead of adding latency to every request. Under load it holds: a k6
run that killed Redis mid-test served 44,991 requests with zero failures
(`loadtest/results/kill-redis.txt`).

## When I'd flip it to fail-closed

When going over the limit is worse than being down. Some cases where I'd run
`-degrade closed`:

| case | mode | why |
|------|------|-----|
| login / password attempts | closed | unlimited tries during an outage is a security hole |
| paid API quotas | closed | letting people blow past what they paid for costs money |
| a fragile backend | closed | failing open just moves the outage downstream |
| public read API | open | availability matters more than a brief enforcement gap |

The rule I use is simple. Fail open when exceeding the limit is an annoyance.
Fail closed when exceeding it is a breach.

## Where the round trip goes under load

`make breakdown` splits a `/check` four ways, at light load and near the knee:

| layer | 2,000 rps | 12,000 rps | growth |
|---|--------:|---------:|-------:|
| lua inside redis | 0.021ms | 0.025ms | 1.2x |
| redis round trip + pool wait | 0.131ms | 4.211ms | 32x |
| handler + policy | 0.001ms | 0.002ms | 2x |
| nginx + go http + wire | 0.689ms | 6.357ms | 9x |
| end to end | 0.842ms | 10.595ms | 13x |

Deciding a request is cheap. Refill, compare, write back, set the TTL, all one
script, about 25µs, and it barely moves between light load and the knee. The
handler adds ~2µs. Together that's under 0.3% of what the caller waits for.

The rest is transport. At 12k rps the client-side Redis call takes 4.2ms waiting
on a script that runs in 0.025ms, so 99.4% of that call is round trip and waiting
for a free connection. What grows under load is queueing, not computation.

One caveat on that table: the last row is a residual, what k6 saw minus the
in-process decision, so it absorbs any error in the other three and the total
matching the client figure is arithmetic rather than confirmation. The real
cross-check is the lua figure, which comes from Redis's own counters and matches
the `evalsha_usec_per_call` column in
[runs.csv](../loadtest/results/saturation/runs.csv) at every rate.

So to make this faster I'd cut round trips, not optimise the script. Pipelining
independent checks, or a short local cache for keys already known to be over
their limit, would move the dominant term. The script is already 25µs.

## What actually limits throughput

The sweep in the [README](../README.md#results) tracks the offered rate to 14k
and falls apart by 16k. Four things could be responsible; three of them aren't.

Start with the load generator, because k6's own settings move the answer more
than I expected. At a fixed 12,000 offered, changing only `preAllocatedVUs`:

| preallocated VUs | achieved | p99 | VUs used |
|-----------------:|---------:|----:|---------:|
| 100 | 11,805 | 66.82ms | 384 |
| 400 | 11,913 | 50.09ms | 263 |
| 1500 | 11,664 | 285.29ms | 2,284 |

Same service, same offered rate, 5.7x spread in p99 from a knob on the load
generator. Too few and a short run spends itself ramping; too many and idle JS
runtimes eat CPU the service needed. Any run where k6 exhausted its VU pool is
excluded from the knee, because it measures k6 rather than the limiter.

Redis isn't it either. Script execution stays flat at ~25µs from 8k through the
cliff, a third of one core at 14k rps, and Redis sat at 62% CPU while the sweep
was falling over.

nginx is. One node hit directly sustains ~15k rps at p99 32ms, while three nodes
behind the proxy sustain ~13.8k and are roughly 9x worse at p99 for the same
offered rate. It's also the largest CPU consumer at saturation, 160% against
~79% per node. The proxy is subtracting capacity, not adding it.

Most of that turned out to be config. The original `nginx.conf` used the default
`worker_connections 512` with no upstream keepalive, so every request opened a
fresh connection to a node. At 12k offered:

| nginx config | achieved | p99 |
|---|--------:|----:|
| original | 6,719 | 2,302ms |
| `worker_connections 8192` + `keepalive 256` | 11,875 | 52.3ms |

Nearly 2x the throughput and 44x better p99 from proxy config alone, which means
a sweep against the original would have reported a ~6.7k ceiling belonging
entirely to nginx.

So the limiter isn't the bottleneck at any rate I measured, and the fix is more
proxy rather than more limiter. I haven't tested that, because the box has no
spare cores left to test it with. Running the generator on a second machine is
the next step, and would settle the VU sensitivity above at the same time.

## What HA costs

Redis holding every counter used to mean losing Redis took the whole thing down:
the availability story stopped at the tier that had the state.
`docker-compose.ha.yml` puts a replica and three sentinels behind the master, and
nodes reach it through the sentinels (`-redis-sentinel`) so they follow a
promotion instead of pointing at a corpse.

Rate limiting is one of the few things where correctness loss is directly
measurable: spend a bucket, kill the master, count how many requests get through
that should have been denied. `make failover` does that, and kills the master
with writes in flight, since the writes at risk are the ones acknowledged in the
last few milliseconds. Its config sets a limit of 100 per hour so the bucket
refills at 0.028 tokens/sec, meaning a 20-second failover returns about half a
token. Anything above that is state the failover lost, not normal refill.

| | |
|---|---|
| enforcement before failover | 100 of 120 allowed, then 0 of 10 |
| promotion time | ~5s (quorum 2 of 3, `down-after-milliseconds 1000`) |
| requests served during the outage | all of them, none failed |
| allowed during the outage window | 4 of 200 |
| allowed after promotion, bucket already spent | 0 |

The last two mean different things. The 4 are the fail-open policy doing its job.
The 0 says the replica had every write that spent the bucket, so the promotion
cost no correctness, but I wouldn't read it as a guarantee: master and replica
are containers on one host here, so replication lag is microseconds. Across
availability zones, or under a heavier write rate, writes the master
acknowledged but hadn't shipped would be lost, and the promoted replica would
start from a staler count, over-admitting by roughly (write rate x lag).

`WAIT 1 <ms>` after each write would close that, at the cost of another round
trip on the hot path, which the section above shows is already the dominant
term. It would roughly double decision latency. For rate limiting that's a bad
trade: over-admitting a handful of requests during a rare failover is cheaper
than paying synchronous replication on every request forever.
