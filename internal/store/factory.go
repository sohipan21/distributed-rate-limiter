package store

import (
	"github.com/redis/go-redis/v9"

	"github.com/sohipan21/distributed-rate-limiter/internal/limiter"
)

// Factory builds redis-backed limiters, so counters hold across every node
// sharing rdb — the in-memory limiter.New counts per process instead
func Factory(rdb *redis.Client) limiter.Factory {
	return func(a limiter.Algorithm, c limiter.Config) limiter.Limiter {
		if a == limiter.SlidingWindowAlgorithm {
			return NewSlidingWindow(rdb, c)
		}
		return NewTokenBucket(rdb, c)
	}
}
