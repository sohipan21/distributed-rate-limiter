# Consistency vs availability

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

## Why I default to fail-open

Allow requests through when Redis is down. The reasoning is simple. A rate
limiter exists to protect the service behind it. If the limiter's own outage
takes that service down, it has done the opposite of its job. For ordinary
throttling like free vs paid tiers or stopping a noisy client, a short window
where limits aren't enforced is annoying but survivable. A full outage is not.

So the default keeps the service up. You lose enforcement for a few seconds
until Redis comes back, and `make demo` shows exactly that. The circuit
breaker keeps that window cheap. It stops hammering a dead Redis after a few
failures instead of adding latency to every request.

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
