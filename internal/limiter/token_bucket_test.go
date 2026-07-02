package limiter

import (
	"sync"
	"testing"
	"time"
)

// fakeClock is a manually-advanced clock so tests never sleep.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newTestBucket wires a TokenBucket to a fake clock.
func newTestBucket(t *testing.T, cfg Config) (*TokenBucket, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	tb := NewTokenBucket(cfg)
	tb.now = clock.Now
	return tb, clock
}

func TestTokenBucketBurstThenDeny(t *testing.T) {
	tb, _ := newTestBucket(t, Config{Limit: 5, Window: time.Second})

	for i := 0; i < 5; i++ {
		d := tb.Allow("k")
		if !d.Allowed {
			t.Fatalf("request %d: denied, want allowed", i+1)
		}
		if want := 5 - (i + 1); d.Remaining != want {
			t.Errorf("request %d: Remaining = %d, want %d", i+1, d.Remaining, want)
		}
	}

	if d := tb.Allow("k"); d.Allowed {
		t.Fatal("request 6: allowed, want denied")
	}
}

func TestTokenBucketRefill(t *testing.T) {
	tb, clock := newTestBucket(t, Config{Limit: 10, Window: time.Second})

	for i := 0; i < 10; i++ {
		tb.Allow("k")
	}
	if d := tb.Allow("k"); d.Allowed {
		t.Fatal("bucket exhausted but request allowed")
	}

	// 10 tokens/sec: 300ms refills exactly 3 tokens.
	clock.Advance(300 * time.Millisecond)
	for i := 0; i < 3; i++ {
		if d := tb.Allow("k"); !d.Allowed {
			t.Fatalf("refilled request %d: denied, want allowed", i+1)
		}
	}
	if d := tb.Allow("k"); d.Allowed {
		t.Fatal("4th request after 300ms allowed, want denied (only 3 tokens refilled)")
	}
}

func TestTokenBucketDenialSemantics(t *testing.T) {
	tb, clock := newTestBucket(t, Config{Limit: 2, Window: time.Second})
	start := clock.Now()

	tb.Allow("k")
	tb.Allow("k")
	d := tb.Allow("k")

	if d.Allowed {
		t.Fatal("allowed, want denied")
	}
	if d.Remaining != 0 {
		t.Errorf("Remaining = %d, want 0", d.Remaining)
	}
	// 2 tokens/sec: the next whole token is 500ms away.
	if want := 500 * time.Millisecond; d.RetryAfter != want {
		t.Errorf("RetryAfter = %v, want %v", d.RetryAfter, want)
	}
	// A full bucket (2 tokens) is 1s away.
	if want := start.Add(time.Second); !d.ResetAt.Equal(want) {
		t.Errorf("ResetAt = %v, want %v", d.ResetAt, want)
	}
}

func TestTokenBucketAllowedSemantics(t *testing.T) {
	tb, clock := newTestBucket(t, Config{Limit: 4, Window: time.Second})
	start := clock.Now()

	d := tb.Allow("k")
	if !d.Allowed {
		t.Fatal("denied, want allowed")
	}
	if d.Remaining != 3 {
		t.Errorf("Remaining = %d, want 3", d.Remaining)
	}
	if d.RetryAfter != 0 {
		t.Errorf("RetryAfter = %v, want 0 on allow", d.RetryAfter)
	}
	// 4 tokens/sec, 1 consumed: full again in 250ms.
	if want := start.Add(250 * time.Millisecond); !d.ResetAt.Equal(want) {
		t.Errorf("ResetAt = %v, want %v", d.ResetAt, want)
	}
}

func TestTokenBucketRefillCappedAtCapacity(t *testing.T) {
	tb, clock := newTestBucket(t, Config{Limit: 3, Window: time.Second})

	tb.Allow("k")
	clock.Advance(time.Hour) // idle far longer than needed to refill

	allowed := 0
	for i := 0; i < 10; i++ {
		if tb.Allow("k").Allowed {
			allowed++
		}
	}
	if allowed != 3 {
		t.Errorf("allowed %d requests after long idle, want 3 (capacity)", allowed)
	}
}

func TestTokenBucketBurstOverride(t *testing.T) {
	// 1/sec steady rate, but a burst capacity of 5.
	tb, _ := newTestBucket(t, Config{Limit: 1, Window: time.Second, Burst: 5})

	for i := 0; i < 5; i++ {
		if d := tb.Allow("k"); !d.Allowed {
			t.Fatalf("burst request %d: denied, want allowed", i+1)
		}
	}
	if d := tb.Allow("k"); d.Allowed {
		t.Fatal("request beyond burst allowed, want denied")
	}
}

func TestTokenBucketPerKeyIsolation(t *testing.T) {
	tb, _ := newTestBucket(t, Config{Limit: 1, Window: time.Second})

	if d := tb.Allow("alice"); !d.Allowed {
		t.Fatal("alice's first request denied")
	}
	if d := tb.Allow("alice"); d.Allowed {
		t.Fatal("alice's second request allowed, want denied")
	}
	// alice being throttled must not affect bob.
	if d := tb.Allow("bob"); !d.Allowed {
		t.Fatal("bob's first request denied, want allowed")
	}
}

func TestTokenBucketConcurrentNoOvercount(t *testing.T) {
	// Frozen clock: no refill during the test, so across all goroutines
	// exactly capacity requests may succeed. Run with -race.
	const capacity = 100
	tb, _ := newTestBucket(t, Config{Limit: capacity, Window: time.Minute})

	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if tb.Allow("k").Allowed {
					mu.Lock()
					allowed++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	if allowed != capacity {
		t.Errorf("allowed %d of 500 concurrent requests, want exactly %d", allowed, capacity)
	}
}

func TestNewTokenBucketRejectsBadConfig(t *testing.T) {
	for _, cfg := range []Config{
		{Limit: 0, Window: time.Second},
		{Limit: -1, Window: time.Second},
		{Limit: 1, Window: 0},
		{Limit: 1, Window: -time.Second},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("NewTokenBucket(%+v) did not panic", cfg)
				}
			}()
			NewTokenBucket(cfg)
		}()
	}
}
