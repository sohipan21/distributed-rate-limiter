# The kill-redis demo

Atomic counting made Redis the one thing every node depends on. So the fair
question is: what happens when it dies? This demo answers it live —
`make demo` brings up the multi-node stack, enforces the limit, kills Redis
mid-traffic, and shows the cluster keep serving instead of falling over.

It's also a smoke test. Each act asserts its own behavior and the script
exits non-zero if the story ever stops being true.

```
make demo
```

## What you see

Three acts, each a fresh identity through nginx (the limit is cluster-wide,
so it doesn't matter which of the three nodes answers):

```
== act 1: redis up, the limit is enforced ==
200 200 200 200 200 200 200 200 200 200 429 429 429

== act 2: kill redis mid-flight ==
redis is down. 13 requests for a fresh identity -- expect all 200:
200 200 200 200 200 200 200 200 200 200 200 200 200
node log:
node3-1  | degraded: redis unreachable, failing open

== act 3: bring redis back ==
node log:
node3-1  | recovered: redis reachable again
200 200 200 200 200 200 200 200 200 200 429 429 429

PASS: enforced (3 denied) -> degraded (0 denied) -> recovered (3 denied)
```

Act 1 is the limiter doing its job: eleven requests, the eleventh gets a 429.
Act 2 is the interesting one — Redis is gone, but every request still gets a
200. The nodes notice Redis is unreachable, log the transition, and fail
open: they'd rather let a request through than take the whole API down over a
limiter outage. Act 3 brings Redis back, the breaker probes and recovers, and
the limit snaps back into place.

## The detail that makes it fast

When Redis first dies, a couple of requests per node pay one 300ms timeout
before that node's circuit breaker trips. After that the breaker
short-circuits — no more waiting on a socket that isn't coming back — so
degraded requests are as quick as healthy ones. On recovery, one probe per
node closes the breaker again.

Failing open is a choice, not the only option: `-degrade closed` flips it, and
the same three acts would show 429s in act 2 instead of 200s. Which default
makes sense, and when you'd want the other, is the whole subject of
[docs/04-tradeoffs.md](04-tradeoffs.md).
