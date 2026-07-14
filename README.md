# Distributed Rate Limiter

[![tests](https://github.com/sohipan21/distributed-rate-limiter/actions/workflows/test.yml/badge.svg)](https://github.com/sohipan21/distributed-rate-limiter/actions/workflows/test.yml)

A rate limiter is the part of a service that decides "has this user made too
many requests, yes or no," and turns away the extras. This one runs as many
copies at once, all sharing a single count, so the limit holds no matter which
server answers a request. It also keeps working when the shared database behind
it goes down.

The rate limiting itself is the easy part. The point of this project is the
harder stuff underneath: counting correctly when many servers are involved, and
deciding what should happen when a dependency fails. Those are the real
distributed-systems questions, and they are what this repo is actually about.

Written in Go. Redis for shared state, gRPC and HTTP APIs, a drop-in Go SDK,
Prometheus and Grafana for monitoring, and Docker Compose to run the whole
thing.

## How it works

Requests come in and get spread across several identical server nodes. The
nodes keep no state of their own. They all talk to one Redis, which holds the
counts and makes every allow-or-deny decision.

```
client ──> node ──┐
client ──> node ──┼──> redis   (holds the counts, decides allow or deny)
client ──> node ──┘
```

Because the count lives in one place, adding more nodes never weakens the
limit. Run one node or twenty and "100 requests per minute" still means 100.
There is also an in-memory mode with no Redis for single-node use, behind the
exact same interface.

## The interesting problems

### Counting correctly across servers

The obvious way to count is: read the current number, add one, write it back.
That breaks the moment two servers do it at the same time. Both read "9 so
far," both decide the request is fine, both write "10." One request just got
lost, and the limit leaks.

The fix is to make the read-and-update a single step that nothing can interrupt.
Here that step is a small script that runs inside Redis itself, which handles
one script at a time. So across every node, the count can only move one request
at a time.

There is a test that proves it. The same flood of concurrent requests runs
against a deliberately naive version and the real one:

```
make up && go test -v -run 'Overcounts|ExactUnder' ./internal/store/
```

The naive version lets 500 requests through a limit of 100. The correct version
allows exactly 100, every time. Redis's own clock is used for all timing too, so
the servers never disagree about when a window starts even if their clocks drift.

### Staying up when Redis goes down

Putting the count in one Redis makes it correct, but now every node depends on
that Redis. So what happens when it is unreachable? There is no free answer. You
can keep the limits correct, or you can keep serving traffic, but not both at
once.

The default is to keep serving (fail open). A rate limiter exists to protect the
service behind it, so it should not be the reason that service goes down. For a
few seconds until Redis returns, requests pass through unlimited. A circuit
breaker makes that cheap by noticing Redis is dead and skipping it instead of
waiting on every request. A flag flips this to the opposite behavior (fail
closed, reject everything) for cases where going over the limit is worse than
being down, like login attempts or paid quotas. The full reasoning is in
[docs/04-tradeoffs.md](docs/04-tradeoffs.md).

You can watch this happen live:

```
make demo
```

It brings the cluster up, shows the limit being enforced, kills Redis in the
middle, shows the service keep answering, then recovers.

### Two algorithms, one interface

Two ways to count, picked per policy:

- **Token bucket**: each user gets a bucket of tokens that refills steadily.
  Cheap, and it tolerates short bursts. The general-purpose default.
- **Sliding window**: tracks the actual timestamps of recent requests. Exact,
  with no way to sneak a double burst around a window boundary, at the cost of a
  bit more memory.

Both sit behind one `Limiter` interface, so the rest of the system does not care
which one a given user is on.

## Try it

```
make up                                    # start redis in docker
go run ./cmd/server -redis localhost:6379  # drop -redis for in-memory mode
```

Ask it about a request:

```
$ curl -si -X POST localhost:8080/check \
    -d '{"identity":"alice","tier":"free","endpoint":"/download"}'
HTTP/1.1 200 OK
X-RateLimit-Limit: 10
X-RateLimit-Remaining: 9
X-RateLimit-Reset: 1783732312

{"allowed":true,"remaining":9,"retry_after_seconds":0,"reset_at":1783732312}
```

Once the caller is over the limit the status becomes `429 Too Many Requests`
with a `Retry-After` header, so well-behaved clients back off on their own.
There is a gRPC API on `:9090` with the same behavior.

## Results

Load tested with k6 against the full cluster (three nodes behind nginx, one
Redis) on a single laptop. These are localhost numbers, so read them as a shape,
not a promise.

| offered | achieved | p50 | p95 | p99 | allowed / denied |
|---------|----------|------|------|------|------------------|
| 300 rps | 300 rps | 1.31ms | 2.97ms | 4.82ms | 5075 / 12903 |
| 1000 rps | 1000 rps | 0.69ms | 0.95ms | 1.39ms | 5223 / 54777 |
| 2000 rps | 2000 rps | 0.58ms | 0.78ms | 1.34ms | 5253 / 114749 |

Allowed stays flat while denied grows, which is the limiter doing its job:
past a point the extra load just becomes 429s. No errors at any level.

![load ramp](docs/img/loadtest-ramp.png)

A fourth run killed Redis mid-test. 44,991 requests, zero failures. Allowed
jumps because the service is failing open while Redis is gone, then enforcement
snaps back when it returns.

![redis killed mid-test](docs/img/loadtest-kill-redis.png)

`make loadtest` reproduces these.

## Use it in your own app

The Go SDK wraps any HTTP handler. A few lines and every request to your server
is checked against the limiter, with over-limit callers getting a 429 before
they reach your code.

```go
import "github.com/sohipan21/distributed-rate-limiter/pkg/sdk"

client, err := sdk.Dial("localhost:9090")
if err != nil {
    log.Fatal(err)
}

// wrap your handler; that's the whole integration
http.ListenAndServe(":8090", sdk.Middleware(client)(yourHandler))
```

By default it identifies callers by the `X-API-Key` header (falling back to
their IP) and uses the request path as the endpoint. Override that with
`sdk.WithKeyFunc`. A runnable version is in
[examples/protected-server](examples/protected-server/main.go).

## How it's built

Go, Redis with Lua scripts for the atomic counting, gRPC with an HTTP shim,
Prometheus and Grafana for metrics, Docker Compose for the multi-node setup, and
k6 for load testing.

```
cmd/server        the service (http + grpc)
internal/limiter  the two algorithms behind one interface
internal/policy   maps a request to its limit (tiers, per-endpoint overrides)
internal/store    redis-backed limiters, the lua scripts, the circuit breaker
internal/grpcapi  grpc server        internal/httpapi  http handlers
internal/metrics  prometheus metrics
pkg/sdk           the drop-in client and middleware
grafana/          dashboard as code   loadtest/  k6 scripts and results
demo/             the kill-redis demo
```

Redis-backed tests skip themselves when Redis is not running, so `make` works
without Docker.
