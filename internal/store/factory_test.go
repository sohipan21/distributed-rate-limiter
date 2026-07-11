package store

import (
	"testing"
	"time"

	"github.com/sohipan21/distributed-rate-limiter/internal/limiter"
)

func TestFactoryReturnsRedisBackedTypes(t *testing.T) {
	// construction never touches redis, so a nil client is fine here
	f := Factory(nil)
	cfg := limiter.Config{Limit: 1, Window: time.Second}

	if _, ok := f(limiter.TokenBucketAlgorithm, cfg).(*TokenBucket); !ok {
		t.Error("factory did not return *store.TokenBucket for token_bucket")
	}
	if _, ok := f(limiter.SlidingWindowAlgorithm, cfg).(*SlidingWindow); !ok {
		t.Error("factory did not return *store.SlidingWindow for sliding_window")
	}
}
