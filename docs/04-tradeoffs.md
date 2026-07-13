# Consistency vs availability

Counting correctly across nodes means one shared counter in Redis (see
[docs/02](02-atomicity.md)). That's the right call, but it has a cost. Every
node now depends on Redis. So when Redis is unreachable, there's a choice with
no clean answer. You can keep the limits correct, or you can keep serving
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
until Redis comes back, and the demo in [docs/03](03-demo.md) shows exactly
that. The circuit breaker keeps that window cheap. It stops hammering a dead
Redis after a few failures instead of adding latency to every request.

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
