package store

import (
	"testing"
	"time"

	"github.com/sohipan21/distributed-rate-limiter/internal/limiter"
)

// the headline: the same workload that let 500/500 through the naive store
// now allows exactly the limit, because the whole check-and-update is one
// atomic lua call

func TestAtomicBucketExactUnderConcurrency(t *testing.T) {
	rdb := testClient(t)
	const limit = 100
	tb := NewTokenBucket(rdb, limiter.Config{Limit: limit, Window: time.Hour})
	key := testKey(t, rdb)

	allowed := hammer(tb, key, 50, 10)
	if allowed != limit {
		t.Errorf("atomic bucket allowed %d of 500 concurrent requests, want exactly %d", allowed, limit)
	}
	t.Logf("atomic bucket: allowed %d of 500 (limit %d)", allowed, limit)
}

func TestAtomicWindowExactUnderConcurrency(t *testing.T) {
	rdb := testClient(t)
	const limit = 100
	sw := NewSlidingWindow(rdb, limiter.Config{Limit: limit, Window: time.Hour})
	key := testKey(t, rdb)

	allowed := hammer(sw, key, 50, 10)
	if allowed != limit {
		t.Errorf("atomic window allowed %d of 500 concurrent requests, want exactly %d", allowed, limit)
	}
	t.Logf("atomic window: allowed %d of 500 (limit %d)", allowed, limit)
}

func TestAtomicBucketSequential(t *testing.T) {
	rdb := testClient(t)
	tb := NewTokenBucket(rdb, limiter.Config{Limit: 5, Window: time.Hour})
	key := testKey(t, rdb)

	for i := 0; i < 5; i++ {
		d := tb.Allow(key)
		if !d.Allowed {
			t.Fatalf("request %d: denied, want allowed", i+1)
		}
		if want := 5 - (i + 1); d.Remaining != want {
			t.Errorf("request %d: Remaining = %d, want %d", i+1, d.Remaining, want)
		}
	}

	d := tb.Allow(key)
	if d.Allowed {
		t.Fatal("request 6: allowed, want denied")
	}
	if d.Remaining != 0 {
		t.Errorf("denied Remaining = %d, want 0", d.Remaining)
	}
	if d.RetryAfter <= 0 {
		t.Errorf("denied RetryAfter = %v, want > 0", d.RetryAfter)
	}
	if !d.ResetAt.After(time.Now()) {
		t.Errorf("denied ResetAt = %v, want in the future", d.ResetAt)
	}
}

func TestAtomicWindowSequential(t *testing.T) {
	rdb := testClient(t)
	sw := NewSlidingWindow(rdb, limiter.Config{Limit: 5, Window: time.Hour})
	key := testKey(t, rdb)

	for i := 0; i < 5; i++ {
		d := sw.Allow(key)
		if !d.Allowed {
			t.Fatalf("request %d: denied, want allowed", i+1)
		}
		if want := 5 - (i + 1); d.Remaining != want {
			t.Errorf("request %d: Remaining = %d, want %d", i+1, d.Remaining, want)
		}
	}

	d := sw.Allow(key)
	if d.Allowed {
		t.Fatal("request 6: allowed, want denied")
	}
	if d.Remaining != 0 {
		t.Errorf("denied Remaining = %d, want 0", d.Remaining)
	}
	if d.RetryAfter <= 0 {
		t.Errorf("denied RetryAfter = %v, want > 0", d.RetryAfter)
	}
	if !d.ResetAt.After(time.Now()) {
		t.Errorf("denied ResetAt = %v, want in the future", d.ResetAt)
	}
}

// coarse timing sanity; precise boundary + TTL tests are day 8
func TestAtomicBucketRefills(t *testing.T) {
	rdb := testClient(t)
	tb := NewTokenBucket(rdb, limiter.Config{Limit: 2, Window: time.Second})
	key := testKey(t, rdb)

	tb.Allow(key)
	tb.Allow(key)
	if tb.Allow(key).Allowed {
		t.Fatal("bucket exhausted but allowed")
	}

	time.Sleep(600 * time.Millisecond) // ~1.2 tokens back at 2/sec
	if !tb.Allow(key).Allowed {
		t.Fatal("denied after refill, want allowed")
	}
}

func TestAtomicWindowFreesAfterExpiry(t *testing.T) {
	rdb := testClient(t)
	sw := NewSlidingWindow(rdb, limiter.Config{Limit: 2, Window: 300 * time.Millisecond})
	key := testKey(t, rdb)

	sw.Allow(key)
	sw.Allow(key)
	if sw.Allow(key).Allowed {
		t.Fatal("window full but allowed")
	}

	time.Sleep(350 * time.Millisecond) // both entries age out
	if !sw.Allow(key).Allowed {
		t.Fatal("denied after entries expired, want allowed")
	}
}
