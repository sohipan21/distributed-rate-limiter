package store

import (
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/sohipan21/distributed-rate-limiter/internal/limiter"
)

// a client pointed at a dead port: every op fails fast, no docker needed
func deadClient() *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:         "localhost:1",
		DialTimeout:  50 * time.Millisecond,
		ReadTimeout:  50 * time.Millisecond,
		WriteTimeout: 50 * time.Millisecond,
		MaxRetries:   -1, // one attempt per op; retrying a dead redis is the breaker's call
	})
}

func TestDegradedModes(t *testing.T) {
	cfg := limiter.Config{Limit: 10, Window: time.Minute}
	rdb := deadClient()
	defer rdb.Close()

	cases := []struct {
		name string
		l    limiter.Limiter
		want bool
	}{
		{"bucket fail-open", NewTokenBucket(rdb, cfg, WithMode(FailOpen)), true},
		{"bucket fail-closed", NewTokenBucket(rdb, cfg, WithMode(FailClosed)), false},
		{"window fail-open", NewSlidingWindow(rdb, cfg, WithMode(FailOpen)), true},
		{"window fail-closed", NewSlidingWindow(rdb, cfg, WithMode(FailClosed)), false},
	}
	for _, tc := range cases {
		d := tc.l.Allow("k")
		if d.Allowed != tc.want {
			t.Errorf("%s: Allowed = %v, want %v", tc.name, d.Allowed, tc.want)
		}
		if !tc.want && d.RetryAfter < time.Second {
			t.Errorf("%s: RetryAfter = %v, want >= 1s", tc.name, d.RetryAfter)
		}
	}
}

func TestBreakerShortCircuitsRequests(t *testing.T) {
	rdb := deadClient()
	defer rdb.Close()
	// long cooldown: once open it stays open for the whole test
	br := NewBreaker(3, time.Minute)
	tb := NewTokenBucket(rdb, limiter.Config{Limit: 10, Window: time.Minute},
		WithMode(FailOpen), WithBreaker(br))

	for i := 0; i < 3; i++ {
		tb.Allow("k")
	}
	if !br.Degraded() {
		t.Fatal("breaker not open after threshold failures")
	}

	// open breaker: no network attempts, so this is near-instant
	start := time.Now()
	for i := 0; i < 50; i++ {
		if !tb.Allow("k").Allowed {
			t.Fatal("fail-open denied while degraded")
		}
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("50 degraded requests took %v, want near-instant (breaker not short-circuiting?)", elapsed)
	}
}

type fakeRedisObserver struct {
	calls  int
	lastOK bool
}

func (f *fakeRedisObserver) ObserveRedis(algorithm string, d time.Duration, ok bool) {
	f.calls++
	f.lastOK = ok
}

func TestObserverSeesRedisErrors(t *testing.T) {
	rdb := deadClient()
	defer rdb.Close()
	fo := &fakeRedisObserver{}
	tb := NewTokenBucket(rdb, limiter.Config{Limit: 10, Window: time.Minute},
		WithMode(FailOpen), WithObserver(fo))

	tb.Allow("k")
	if fo.calls != 1 || fo.lastOK {
		t.Errorf("observer calls = %d lastOK = %v, want 1 call with ok=false", fo.calls, fo.lastOK)
	}
}

func TestFactorySharesOneBreaker(t *testing.T) {
	rdb := deadClient()
	defer rdb.Close()
	br := NewBreaker(3, time.Minute)
	f := Factory(rdb, WithMode(FailOpen), WithBreaker(br))

	bucket := f(limiter.TokenBucketAlgorithm, limiter.Config{Limit: 10, Window: time.Minute})
	window := f(limiter.SlidingWindowAlgorithm, limiter.Config{Limit: 10, Window: time.Minute})

	// failures through the bucket trip the breaker for the window too
	for i := 0; i < 3; i++ {
		bucket.Allow("k")
	}
	if !br.Degraded() {
		t.Fatal("breaker not open")
	}
	start := time.Now()
	if !window.Allow("k").Allowed {
		t.Fatal("window not failing open on shared breaker")
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("window request took %v, want short-circuited", elapsed)
	}
}
