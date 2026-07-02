package limiter

import (
	"sync"
	"time"
)

// TokenBucket is an in-memory token-bucket rate limiter.
// Each key gets a bucket holding up to burst tokens. Tokens refill
// continuously at Limit/Window, and every allowed request consumes one.
// A request is denied when its key's bucket is empty. Tokens are tracked
// fractionally so refill is smooth rather than stepped.
//
// It is safe for concurrent use.
type TokenBucket struct {
	cfg  Config
	rate float64 // tokens refilled per second

	mu      sync.Mutex
	buckets map[string]*bucket

	// now returns the current time; injectable for deterministic tests.
	now func() time.Time
}

// bucket is the per-key state.
type bucket struct {
	tokens float64   // tokens currently available
	last   time.Time // when tokens was last updated
}

// NewTokenBucket returns a token-bucket limiter enforcing cfg.
// It panics if cfg.Limit or cfg.Window is not positive, since a
// zero-rate limiter is a configuration bug, not a runtime condition.
func NewTokenBucket(cfg Config) *TokenBucket {
	if cfg.Limit <= 0 {
		panic("limiter: Config.Limit must be positive")
	}
	if cfg.Window <= 0 {
		panic("limiter: Config.Window must be positive")
	}
	return &TokenBucket{
		cfg:     cfg,
		rate:    float64(cfg.Limit) / cfg.Window.Seconds(),
		buckets: make(map[string]*bucket),
		now:     time.Now,
	}
}

// Allow reports whether the request identified by key may proceed,
// consuming one token if so.
func (tb *TokenBucket) Allow(key string) Decision {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := tb.now()
	capacity := float64(tb.cfg.burst())

	b, ok := tb.buckets[key]
	if !ok {
		b = &bucket{tokens: capacity, last: now}
		tb.buckets[key] = b
	}

	// refill for the time elapsed since the last update.
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens = min(capacity, b.tokens+elapsed.Seconds()*tb.rate)
	}
	b.last = now

	d := Decision{ResetAt: now.Add(tb.durationFor(capacity - b.tokens))}
	if b.tokens < 1 {
		// Denied: wait until a whole token has refilled.
		d.RetryAfter = tb.durationFor(1 - b.tokens)
		return d
	}

	b.tokens--
	d.Allowed = true
	d.Remaining = int(b.tokens)
	d.ResetAt = now.Add(tb.durationFor(capacity - b.tokens))
	return d
}

// durationFor converts a token deficit into the time needed to refill it.
func (tb *TokenBucket) durationFor(tokens float64) time.Duration {
	if tokens <= 0 {
		return 0
	}
	return time.Duration(tokens / tb.rate * float64(time.Second))
}
