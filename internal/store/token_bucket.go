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
}

func NewTokenBucket(rdb *redis.Client, cfg limiter.Config) *TokenBucket {
	if err := cfg.Validate(); err != nil {
		panic(err)
	}
	return &TokenBucket{rdb: rdb, cfg: cfg, script: redis.NewScript(tokenBucketScript)}
}

func (t *TokenBucket) Allow(key string) limiter.Decision {
	burst := t.cfg.Burst
	if burst <= 0 {
		burst = t.cfg.Limit
	}
	res, err := t.script.Run(context.Background(), t.rdb,
		[]string{key},
		t.cfg.Limit, t.cfg.Window.Microseconds(), burst,
	).Int64Slice()
	if err != nil {
		return limiter.Decision{} // fail closed; week 3 makes this configurable
	}
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
