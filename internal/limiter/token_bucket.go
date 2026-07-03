package limiter

import (
	"sync"
	"time"
)

// in-memory token bucket limiter, safe for concurrent use. each key gets a
// bucket of up to burst tokens refilling at Limit/Window; a request costs one
// token and is denied when the bucket is empty
type TokenBucket struct {
	cfg  Config
	rate float64 // tokens per second

	mu      sync.Mutex
	buckets map[string]*bucket

	now func() time.Time // injectable for tests
}

type bucket struct {
	tokens float64
	last   time.Time
}

// panics on an invalid config — a config bug, not a runtime condition
func NewTokenBucket(cfg Config) *TokenBucket {
	if err := cfg.Validate(); err != nil {
		panic(err)
	}
	return &TokenBucket{
		cfg:     cfg,
		rate:    float64(cfg.Limit) / cfg.Window.Seconds(),
		buckets: make(map[string]*bucket),
		now:     time.Now,
	}
}

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

	// refill for the time elapsed since the last update
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens = min(capacity, b.tokens+elapsed.Seconds()*tb.rate)
	}
	b.last = now

	d := Decision{ResetAt: now.Add(tb.durationFor(capacity - b.tokens))}
	if b.tokens < 1 {
		// denied: wait for a whole token to refill
		d.RetryAfter = tb.durationFor(1 - b.tokens)
		return d
	}

	b.tokens--
	d.Allowed = true
	d.Remaining = int(b.tokens)
	d.ResetAt = now.Add(tb.durationFor(capacity - b.tokens))
	return d
}

// how long refilling that many tokens takes
func (tb *TokenBucket) durationFor(tokens float64) time.Duration {
	if tokens <= 0 {
		return 0
	}
	return time.Duration(tokens / tb.rate * float64(time.Second))
}
