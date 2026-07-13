# Distributed Rate Limiter

A rate limiter that runs as stateless service nodes sharing one Redis, so the
limit holds no matter which node a request lands on. The counting happens in
Lua scripts executed atomically inside Redis, on Redis's clock. Those are the
two things that make distributed counting actually correct (there's a
[writeup](docs/02-atomicity.md) with the receipts).

```
client ──> service node ──┐
client ──> service node ──┼──> redis (lua: check-and-update, one atomic op,
client ──> service node ──┘           redis TIME as the only clock)
```

Nodes hold no limiter state; run one or twenty. There's also an in-memory
backend for single-node use, same interface, no Redis.

**Done so far:** token bucket + sliding window behind one interface, policy
layer (tiers + per-endpoint overrides), atomic Lua scripts, HTTP and gRPC APIs
with standard rate-limit headers, multi-node compose behind nginx, graceful
degradation (fail-open/fail-closed) when Redis dies, k6 baseline numbers.
**Next up:** Prometheus/Grafana dashboards, full load-test report.

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
one. Same concurrent workload against both. The naive version allowed 500
requests through a limit of 100, the atomic version allows exactly 100:

```
make up && go test -v -run 'Overcounts|ExactUnder' ./internal/store/
```

For the failure side, `make demo` brings the cluster up, kills Redis
mid-traffic, and shows it keep serving instead of crashing. Writeups:
[docs/03](docs/03-demo.md) for the demo, [docs/04](docs/04-tradeoffs.md) for
the consistency-vs-availability call.

## Development

| target | does |
|--------|------|
| `make` | fmt + vet + tests (race detector on) |
| `make bench` | algorithm benchmarks |
| `make up` / `make down` | redis via docker compose |
| `make loadtest` | k6 against a running server |
| `make demo` | kill-redis degradation demo |

Redis-backed tests skip when Redis isn't running, so `make` works without
Docker.

## Numbers so far

Decision latency at 300 rps (single M2 Pro, localhost): in-memory p50 384µs,
redis-backed p50 1.12ms / p99 3.65ms. Details in
[loadtest/README.md](loadtest/README.md), algorithm benchmarks in
[docs/01-algorithms.md](docs/01-algorithms.md).
