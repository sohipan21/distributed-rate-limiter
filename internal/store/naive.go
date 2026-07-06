// Package store provides redis-backed limiter implementations, so multiple
// service nodes can share one set of counters
package store

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/sohipan21/distributed-rate-limiter/internal/limiter"
)

// NaiveTokenBucket is a redis-backed token bucket done the WRONG way on
// purpose: read state, compute locally, write state back. two round trips
// with a gap in between, so concurrent requests read the same tokens and
// both spend them — the counter leaks past the limit under load. it exists
// to demonstrate the race; see TestNaiveBucketOvercountsUnderConcurrency.
// the atomic lua implementation is the real one
type NaiveTokenBucket struct {
	rdb *redis.Client
	cfg limiter.Config
}

func NewNaiveTokenBucket(rdb *redis.Client, cfg limiter.Config) *NaiveTokenBucket {
	if err := cfg.Validate(); err != nil {
		panic(err)
	}
	return &NaiveTokenBucket{rdb: rdb, cfg: cfg}
}

func (n *NaiveTokenBucket) Allow(key string) limiter.Decision {
	ctx := context.Background()
	now := time.Now() // client-side clock, also naive: redis TIME comes with the lua version
	capacity := float64(n.burst())
	rate := float64(n.cfg.Limit) / n.cfg.Window.Seconds()

	// round trip 1: read
	vals, err := n.rdb.HGetAll(ctx, key).Result()
	if err != nil {
		// fail closed for now; graceful degradation is week 3 work
		return limiter.Decision{}
	}

	tokens := capacity
	if raw, ok := vals["tokens"]; ok {
		tokens, _ = strconv.ParseFloat(raw, 64)
		lastNanos, _ := strconv.ParseInt(vals["last"], 10, 64)
		elapsed := now.Sub(time.Unix(0, lastNanos))
		if elapsed > 0 {
			tokens = min(capacity, tokens+elapsed.Seconds()*rate)
		}
	}

	// ...another request can read the same state right here...

	d := limiter.Decision{}
	if tokens >= 1 {
		tokens--
		d.Allowed = true
		d.Remaining = int(tokens)
	} else {
		d.RetryAfter = n.durationFor(1 - tokens)
	}
	d.ResetAt = now.Add(n.durationFor(capacity - tokens))

	// round trip 2: write back, clobbering whatever happened in between
	n.rdb.HSet(ctx, key, "tokens", tokens, "last", now.UnixNano())
	return d
}

func (n *NaiveTokenBucket) burst() int {
	if n.cfg.Burst > 0 {
		return n.cfg.Burst
	}
	return n.cfg.Limit
}

func (n *NaiveTokenBucket) durationFor(tokens float64) time.Duration {
	if tokens <= 0 {
		return 0
	}
	rate := float64(n.cfg.Limit) / n.cfg.Window.Seconds()
	return time.Duration(tokens / rate * float64(time.Second))
}
