# Token bucket vs sliding window

The limiter ships both algorithms behind one interface (`internal/limiter`).
That's not indecision — they answer the question "no more than N per window"
differently, and the difference shows up in real traffic. Which one you want
depends on whether you care more about tolerating bursts or strictly capping
them.

## Token bucket

Each key gets a bucket holding up to `burst` tokens. Tokens drip back in at a
steady rate (`limit / window`), and every allowed request takes one. Empty
bucket, denied request.

```
      refill: limit/window tokens per sec
        |
        v
    +--------+
    | o o o  |  capacity = burst (defaults to limit)
    +--------+
        |
        v
   each request takes one token; empty = 429
```

State per key is two numbers: token count and a timestamp. On every request I
compute how much drip happened since the last one and top the bucket up before
checking. Tokens are fractional, so the refill is smooth — you don't wait 6
seconds and get nothing, then suddenly get a full second's worth.

## Sliding window log

Keep the timestamps of every request from the last window. If fewer than
`limit` remain, allow and record; otherwise deny. Old entries fall off as time
moves.

```
             window (60s), sliding ->
   |----------------------------------------|
   x    x  x       x x           x      x   now
   ^
   entries older than (now - window) get pruned
```

This is the exact version — never more than `limit` requests in *any*
window-sized span, no matter how you align it. The cheaper "counter per fixed
window" approximation lets a caller double up around the boundary (N requests
at 0:59, N more at 1:01). I went with the log because the correctness claim is
clean, and because it maps one-to-one onto the Redis sorted-set implementation
coming in week 2 — same prune/count/append dance, just in Lua.

## Where they actually disagree

Fill either limiter to the brim and both deny. The difference is what happens
next. The token bucket starts drip-refilling immediately, so a burst is
forgiven gradually. The sliding window won't let anyone in until the burst
literally ages out of the window.

There's a pinned test for this (`recovery after burst diverges` in
`internal/limiter/compare_test.go`): after a full burst at limit 4/sec, the
bucket is letting requests through again 500ms later, while the window is
still saying no.

You can see it from the outside too. Hammering the demo server, the token
bucket tier came back with `Retry-After: 6` while the sliding-window endpoint
said `Retry-After: 60` — the bucket needs one token's worth of drip, the
window needs the whole burst to expire.

## Numbers

`go test -bench=. -benchmem ./internal/limiter/` on an Apple M2 Pro, go 1.26:

| benchmark                  | ns/op | B/op | allocs/op |
|----------------------------|------:|-----:|----------:|
| TokenBucketAllow           | 126   | 0    | 0         |
| SlidingWindowAllow         | 194   | 0    | 0         |
| TokenBucketAllowDenied     | 120   | 0    | 0         |
| SlidingWindowAllowDenied   | 107   | 0    | 0         |
| TokenBucketParallel        | 379   | 3    | 1         |
| SlidingWindowParallel      | 493   | 3    | 1         |

Both are zero-allocation at steady state. The window pays ~50% more per check
on the hot path — it's pruning and appending a timestamp log instead of doing
arithmetic on two floats — but at ~200ns a check, neither is going to be the
bottleneck; the Redis round trip in week 2 will dwarf both. The parallel rows
are mostly measuring mutex contention (the one alloc there is the benchmark
building key strings, not the limiter).

The real cost difference is memory, not time: the bucket stores two numbers
per key forever, the window stores one entry per request until it ages out.
At limit 10,000/min that's 10,000 timestamps per hot key. Worth knowing before
picking the window for very high limits.

## Picking one

Token bucket is the default: cheap, constant memory, and bursts get absorbed
instead of punished — which is usually what you want for a public API.

Sliding window is for when the cap is a promise, not a target. Billing-adjacent
quotas, expensive downstream calls, anywhere "10 per minute" must mean exactly
that in every 60-second span.
