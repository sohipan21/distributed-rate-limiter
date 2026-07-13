package store

import (
	"context"
	_ "embed"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/sohipan21/distributed-rate-limiter/internal/limiter"
)

//go:embed lua/token_bucket.lua
var tokenBucketScript string

// TokenBucket is the real redis-backed token bucket: check-and-update runs
// as one atomic lua script inside redis, so concurrent requests can't lose
// updates the way NaiveTokenBucket does
type TokenBucket struct {
	rdb    *redis.Client
	cfg    limiter.Config
	script *redis.Script
	opts   options
}

func NewTokenBucket(rdb *redis.Client, cfg limiter.Config, opts ...Option) *TokenBucket {
	if err := cfg.Validate(); err != nil {
		panic(err)
	}
	tb := &TokenBucket{rdb: rdb, cfg: cfg, script: redis.NewScript(tokenBucketScript)}
	for _, fn := range opts {
		fn(&tb.opts)
	}
	return tb
}

func (t *TokenBucket) Allow(key string) limiter.Decision {
	if !t.opts.breaker.allow() {
		return degradedDecision(t.opts.mode)
	}
	burst := t.cfg.Burst
	if burst <= 0 {
		burst = t.cfg.Limit
	}
	start := time.Now()
	res, err := t.script.Run(context.Background(), t.rdb,
		[]string{key},
		t.cfg.Limit, t.cfg.Window.Microseconds(), burst,
	).Int64Slice()
	if t.opts.observer != nil {
		t.opts.observer.ObserveRedis(string(limiter.TokenBucketAlgorithm), time.Since(start), err == nil)
	}
	if err != nil {
		t.opts.breaker.failure()
		return degradedDecision(t.opts.mode)
	}
	t.opts.breaker.success()
	return decode(res)
}

// both scripts return {allowed, remaining, retry_after_us, reset_at_us};
// reset_at is an absolute redis-clock timestamp, so every node agrees on it
func decode(res []int64) limiter.Decision {
	if len(res) != 4 {
		return limiter.Decision{}
	}
	return limiter.Decision{
		Allowed:    res[0] == 1,
		Remaining:  int(res[1]),
		RetryAfter: time.Duration(res[2]) * time.Microsecond,
		ResetAt:    time.UnixMicro(res[3]),
	}
}
