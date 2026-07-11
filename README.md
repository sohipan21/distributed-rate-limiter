# Distributed Rate Limiter

A rate limiter that runs as stateless service nodes sharing one Redis, so the
limit holds no matter which node a request lands on. The counting happens in
Lua scripts executed atomically inside Redis, on Redis's clock — the two
things that make distributed counting actually correct (there's a
[writeup](docs/02-atomicity.md) with the receipts).

```
client ──> service node ──┐
client ──> service node ──┼──> redis (lua: check-and-update, one atomic op,
client ──> service node ──┘           redis TIME as the only clock)
```

Nodes hold no limiter state; run one or twenty. There's also an in-memory
backend for single-node use — same interface, no Redis.

**Done so far:** token bucket + sliding window behind one interface, policy
layer (tiers + per-endpoint overrides), atomic Lua scripts, HTTP API with
standard rate-limit headers, k6 baseline numbers.
**Next up:** multi-node compose, gRPC API, graceful degradation when Redis
dies, Prometheus/Grafana.

## Quickstart

```
make up                                    # redis in docker
go run ./cmd/server -redis localhost:6379  # omit -redis for in-memory
```

```
$ curl -si -X POST localhost:8080/check \
    -d '{"identity":"alice","tier":"free","endpoint":"/download"}'
HTTP/1.1 200 OK
X-RateLimit-Limit: 10
X-RateLimit-Remaining: 9
X-RateLimit-Reset: 1783732312

{"allowed":true,"remaining":9,"retry_after_seconds":0,"reset_at":1783732312}
```

Past the limit you get a `429` with `Retry-After`. Clients that respect the
headers back off on their own.

## The party trick

The repo keeps a deliberately racy read-modify-write store next to the atomic
one. Same concurrent workload against both — the naive version allowed 500
requests through a limit of 100, the atomic version allows exactly 100:

```
make up && go test -v -run 'Overcounts|ExactUnder' ./internal/store/
```

## Development

| target | does |
|--------|------|
| `make` | fmt + vet + tests (race detector on) |
| `make bench` | algorithm benchmarks |
| `make up` / `make down` | redis via docker compose |
| `make loadtest` | k6 against a running server |

Redis-backed tests skip when Redis isn't running, so `make` works without
Docker.

## Numbers so far

Decision latency at 300 rps (single M2 Pro, localhost): in-memory p50 384µs,
redis-backed p50 1.12ms / p99 3.65ms — details in
[loadtest/README.md](loadtest/README.md), algorithm benchmarks in
[docs/01-algorithms.md](docs/01-algorithms.md).
