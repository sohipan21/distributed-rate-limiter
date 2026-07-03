package limiter

import (
	"sync"
	"testing"
	"time"
)

func newTestWindow(t *testing.T, cfg Config) (*SlidingWindow, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	sw := NewSlidingWindow(cfg)
	sw.now = clock.Now
	return sw, clock
}

func TestSlidingWindowFillThenDeny(t *testing.T) {
	sw, _ := newTestWindow(t, Config{Limit: 5, Window: time.Second})

	for i := 0; i < 5; i++ {
		d := sw.Allow("k")
		if !d.Allowed {
			t.Fatalf("request %d: denied, want allowed", i+1)
		}
		if want := 5 - (i + 1); d.Remaining != want {
			t.Errorf("request %d: Remaining = %d, want %d", i+1, d.Remaining, want)
		}
	}

	if d := sw.Allow("k"); d.Allowed {
		t.Fatal("request 6: allowed, want denied")
	}
}

func TestSlidingWindowBoundary(t *testing.T) {
	sw, clock := newTestWindow(t, Config{Limit: 3, Window: time.Second})

	for i := 0; i < 3; i++ {
		sw.Allow("k")
	}

	// entries still count at Window-1ns, expire at exactly Window
	clock.Advance(time.Second - time.Nanosecond)
	if d := sw.Allow("k"); d.Allowed {
		t.Fatal("allowed at Window-1ns, want denied")
	}

	clock.Advance(time.Nanosecond)
	for i := 0; i < 3; i++ {
		if d := sw.Allow("k"); !d.Allowed {
			t.Fatalf("request %d after full window: denied, want allowed", i+1)
		}
	}
}

func TestSlidingWindowGradualExpiry(t *testing.T) {
	// requests spread across the window free up one at a time, not all at once
	sw, clock := newTestWindow(t, Config{Limit: 3, Window: time.Second})

	sw.Allow("k") // t=0
	clock.Advance(300 * time.Millisecond)
	sw.Allow("k") // t=300ms
	clock.Advance(300 * time.Millisecond)
	sw.Allow("k") // t=600ms

	clock.Advance(300 * time.Millisecond)
	if d := sw.Allow("k"); d.Allowed {
		t.Fatal("t=900ms: allowed, want denied")
	}

	clock.Advance(100 * time.Millisecond)
	if d := sw.Allow("k"); !d.Allowed {
		t.Fatal("t=1s: denied, want allowed (oldest expired)")
	}
	if d := sw.Allow("k"); d.Allowed {
		t.Fatal("t=1s: second request allowed, want denied (only one slot freed)")
	}

	clock.Advance(300 * time.Millisecond)
	if d := sw.Allow("k"); !d.Allowed {
		t.Fatal("t=1.3s: denied, want allowed (next oldest expired)")
	}
}

func TestSlidingWindowDenialSemantics(t *testing.T) {
	sw, clock := newTestWindow(t, Config{Limit: 2, Window: time.Second})
	start := clock.Now()

	sw.Allow("k") // t=0
	clock.Advance(100 * time.Millisecond)
	sw.Allow("k") // t=100ms
	clock.Advance(100 * time.Millisecond)
	d := sw.Allow("k") // t=200ms

	if d.Allowed {
		t.Fatal("allowed, want denied")
	}
	if d.Remaining != 0 {
		t.Errorf("Remaining = %d, want 0", d.Remaining)
	}
	// oldest entry frees a slot at t=1s, newest returns full quota at t=1.1s
	if want := 800 * time.Millisecond; d.RetryAfter != want {
		t.Errorf("RetryAfter = %v, want %v", d.RetryAfter, want)
	}
	if want := start.Add(1100 * time.Millisecond); !d.ResetAt.Equal(want) {
		t.Errorf("ResetAt = %v, want %v", d.ResetAt, want)
	}
}

func TestSlidingWindowAllowedSemantics(t *testing.T) {
	sw, clock := newTestWindow(t, Config{Limit: 4, Window: time.Second})
	start := clock.Now()

	d := sw.Allow("k")
	if !d.Allowed {
		t.Fatal("denied, want allowed")
	}
	if d.Remaining != 3 {
		t.Errorf("Remaining = %d, want 3", d.Remaining)
	}
	if d.RetryAfter != 0 {
		t.Errorf("RetryAfter = %v, want 0 on allow", d.RetryAfter)
	}
	if want := start.Add(time.Second); !d.ResetAt.Equal(want) {
		t.Errorf("ResetAt = %v, want %v", d.ResetAt, want)
	}
}

func TestSlidingWindowPerKeyIsolation(t *testing.T) {
	sw, _ := newTestWindow(t, Config{Limit: 1, Window: time.Second})

	if d := sw.Allow("alice"); !d.Allowed {
		t.Fatal("alice's first request denied")
	}
	if d := sw.Allow("alice"); d.Allowed {
		t.Fatal("alice's second request allowed, want denied")
	}
	if d := sw.Allow("bob"); !d.Allowed {
		t.Fatal("bob's first request denied, want allowed")
	}
}

func TestSlidingWindowConcurrentNoOvercount(t *testing.T) {
	// frozen clock: nothing expires mid-test, so exactly Limit may succeed
	const limit = 100
	sw, _ := newTestWindow(t, Config{Limit: limit, Window: time.Minute})

	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if sw.Allow("k").Allowed {
					mu.Lock()
					allowed++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	if allowed != limit {
		t.Errorf("allowed %d of 500 concurrent requests, want exactly %d", allowed, limit)
	}
}

func TestNewSlidingWindowRejectsBadConfig(t *testing.T) {
	for _, cfg := range []Config{
		{Limit: 0, Window: time.Second},
		{Limit: -1, Window: time.Second},
		{Limit: 1, Window: 0},
		{Limit: 1, Window: -time.Second},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("NewSlidingWindow(%+v) did not panic", cfg)
				}
			}()
			NewSlidingWindow(cfg)
		}()
	}
}
