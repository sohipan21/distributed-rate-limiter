package store

import (
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/sohipan21/distributed-rate-limiter/internal/limiter"
)

// Factory builds redis-backed limiters, so counters hold across every node
// sharing rdb — the in-memory limiter.New counts per process instead.
// every limiter it builds shares one breaker: the first to notice a dead
// redis flips them all into degraded mode
func Factory(rdb *redis.Client, opts ...Option) limiter.Factory {
	var o options
	for _, fn := range opts {
		fn(&o)
	}
	if o.breaker == nil {
		o.breaker = NewBreaker(3, time.Second)
	}
	shared := []Option{WithMode(o.mode), WithBreaker(o.breaker), WithObserver(o.observer)}

	return func(a limiter.Algorithm, c limiter.Config) limiter.Limiter {
		if a == limiter.SlidingWindowAlgorithm {
			return NewSlidingWindow(rdb, c, shared...)
		}
		return NewTokenBucket(rdb, c, shared...)
	}
}
