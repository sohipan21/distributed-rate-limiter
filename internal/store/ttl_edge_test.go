package store

import (
	"context"
	"testing"
	"time"

	"github.com/sohipan21/distributed-rate-limiter/internal/limiter"
)

// ttl behavior: keys must not outlive their usefulness, or redis memory
// grows with every key ever seen

func TestBucketSetsTTL(t *testing.T) {
	rdb := testClient(t)
	tb := NewTokenBucket(rdb, limiter.Config{Limit: 10, Window: time.Minute})
	key := testKey(t, rdb)

	tb.Allow(key)
	ttl := rdb.PTTL(context.Background(), key).Val()
	// full-refill horizon: one window when burst == limit
	if ttl <= 0 || ttl > time.Minute+time.Second {
		t.Errorf("bucket PTTL = %v, want in (0, ~1m]", ttl)
	}
}

func TestWindowSetsTTL(t *testing.T) {
	rdb := testClient(t)
	sw := NewSlidingWindow(rdb, limiter.Config{Limit: 10, Window: time.Minute})
	key := testKey(t, rdb)

	sw.Allow(key)
	ttl := rdb.PTTL(context.Background(), key).Val()
	if ttl <= 0 || ttl > time.Minute {
		t.Errorf("window PTTL = %v, want in (0, 1m]", ttl)
	}
}

func TestIdleKeysExpire(t *testing.T) {
	rdb := testClient(t)
	ctx := context.Background()
	tb := NewTokenBucket(rdb, limiter.Config{Limit: 2, Window: 300 * time.Millisecond})
	sw := NewSlidingWindow(rdb, limiter.Config{Limit: 2, Window: 300 * time.Millisecond})
	bkey, wkey := testKey(t, rdb), testKey(t, rdb)

	tb.Allow(bkey)
	sw.Allow(wkey)
	time.Sleep(500 * time.Millisecond)

	if n := rdb.Exists(ctx, bkey).Val(); n != 0 {
		t.Error("bucket key still exists after ttl, want expired")
	}
	if n := rdb.Exists(ctx, wkey).Val(); n != 0 {
		t.Error("window key still exists after ttl, want expired")
	}
}

func TestTTLRefreshesOnActivity(t *testing.T) {
	rdb := testClient(t)
	ctx := context.Background()
	sw := NewSlidingWindow(rdb, limiter.Config{Limit: 10, Window: time.Second})
	key := testKey(t, rdb)

	sw.Allow(key)
	time.Sleep(400 * time.Millisecond)
	before := rdb.PTTL(ctx, key).Val()
	sw.Allow(key)
	after := rdb.PTTL(ctx, key).Val()

	if after <= before {
		t.Errorf("PTTL after activity = %v, want > %v (refreshed)", after, before)
	}
}

// edge cases around the limit boundary

func TestExactlyAtLimit(t *testing.T) {
	rdb := testClient(t)
	cfg := limiter.Config{Limit: 3, Window: time.Hour}
	for name, l := range map[string]limiter.Limiter{
		"bucket": NewTokenBucket(rdb, cfg),
		"window": NewSlidingWindow(rdb, cfg),
	} {
		key := testKey(t, rdb)
		var last limiter.Decision
		for i := 0; i < 3; i++ {
			last = l.Allow(key)
			if !last.Allowed {
				t.Fatalf("%s request %d: denied, want allowed", name, i+1)
			}
		}
		// the last allowed request already reports an empty quota
		if last.Remaining != 0 {
			t.Errorf("%s: Remaining on last allowed = %d, want 0", name, last.Remaining)
		}
		if d := l.Allow(key); d.Allowed {
			t.Errorf("%s request 4: allowed, want denied", name)
		}
	}
}

func TestEmptyBucketRecovery(t *testing.T) {
	rdb := testClient(t)
	tb := NewTokenBucket(rdb, limiter.Config{Limit: 2, Window: 400 * time.Millisecond})
	key := testKey(t, rdb)

	tb.Allow(key)
	tb.Allow(key)
	if tb.Allow(key).Allowed {
		t.Fatal("drained bucket allowed a request")
	}

	// idle past the refill horizon: key expires, fresh bucket comes back full
	time.Sleep(600 * time.Millisecond)
	d := tb.Allow(key)
	if !d.Allowed {
		t.Fatal("denied after full refill window, want allowed")
	}
	if d.Remaining != 1 {
		t.Errorf("Remaining after recovery = %d, want 1 (full capacity minus this request)", d.Remaining)
	}
}

func TestFractionalRefillNoLeak(t *testing.T) {
	rdb := testClient(t)
	// 1 token per 30s: 300ms accrues 0.01 tokens, nowhere near a whole one
	tb := NewTokenBucket(rdb, limiter.Config{Limit: 2, Window: time.Minute})
	key := testKey(t, rdb)

	tb.Allow(key)
	tb.Allow(key)
	time.Sleep(300 * time.Millisecond)
	if tb.Allow(key).Allowed {
		t.Error("fractional refill granted a whole token")
	}
}

func TestBucketBurstOverride(t *testing.T) {
	rdb := testClient(t)
	tb := NewTokenBucket(rdb, limiter.Config{Limit: 1, Window: time.Minute, Burst: 5})
	key := testKey(t, rdb)

	for i := 0; i < 5; i++ {
		if d := tb.Allow(key); !d.Allowed {
			t.Fatalf("burst request %d: denied, want allowed", i+1)
		}
	}
	if d := tb.Allow(key); d.Allowed {
		t.Fatal("request beyond burst allowed, want denied")
	}
}

func TestWindowRollover(t *testing.T) {
	rdb := testClient(t)
	sw := NewSlidingWindow(rdb, limiter.Config{Limit: 3, Window: time.Second})
	key := testKey(t, rdb)

	sw.Allow(key) // t=0
	sw.Allow(key) // t=0
	time.Sleep(400 * time.Millisecond)
	sw.Allow(key) // t=400ms, window now full
	if sw.Allow(key).Allowed {
		t.Fatal("full window allowed a request")
	}

	// t~1.1s: the two t=0 entries aged out, the t=400ms one hasn't —
	// rollover frees exactly the expired slots
	time.Sleep(700 * time.Millisecond)
	for i := 0; i < 2; i++ {
		if d := sw.Allow(key); !d.Allowed {
			t.Fatalf("freed slot %d: denied, want allowed", i+1)
		}
	}
	if sw.Allow(key).Allowed {
		t.Fatal("third request allowed, want denied (t=400ms entry still in window)")
	}
}
