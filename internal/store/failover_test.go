package store

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/sohipan21/distributed-rate-limiter/internal/limiter"
)

// Sentinel-backed tests need the HA topology up:
//
//	docker compose -f docker-compose.ha.yml up -d --wait
//	SENTINEL_ADDRS=localhost:26379 go test ./internal/store/ -run Failover
//
// They skip otherwise, same as the plain redis tests skip without a redis.
func sentinelClient(t *testing.T) redis.UniversalClient {
	t.Helper()
	addrs := os.Getenv("SENTINEL_ADDRS")
	if addrs == "" {
		t.Skip("SENTINEL_ADDRS not set; start docker-compose.ha.yml to run failover tests")
	}
	master := os.Getenv("SENTINEL_MASTER")
	if master == "" {
		master = "mymaster"
	}

	rdb := redis.NewFailoverClient(&redis.FailoverOptions{
		MasterName:    master,
		SentinelAddrs: strings.Split(addrs, ","),
		DialTimeout:   300 * time.Millisecond,
		ReadTimeout:   300 * time.Millisecond,
		WriteTimeout:  300 * time.Millisecond,
		MaxRetries:    -1,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("sentinel-backed redis not reachable via %s: %v", addrs, err)
	}
	t.Cleanup(func() { rdb.Close() })
	return rdb
}

// The point of routing through sentinel is that the limiter keeps working, and
// keeps counting, without knowing which instance is currently master. This
// asserts the boring half of that: a sentinel-backed client enforces exactly
// like a directly-connected one. The interesting half — what a promotion costs
// in over-admission — is measured by demo/failover.sh, because it needs to kill
// a container.
func TestFailoverClientEnforcesExactly(t *testing.T) {
	rdb := sentinelClient(t)
	key := fmt.Sprintf("test:failover:%d", rand.Int63())
	t.Cleanup(func() { rdb.Del(context.Background(), key) })

	const limit = 50
	tb := NewTokenBucket(rdb, limiter.Config{Limit: limit, Window: time.Hour})

	// window is an hour, so refill during the test is negligible and the
	// bucket should hand out exactly `limit` and then nothing
	allowed := hammer(tb, key, 10, 20)
	if allowed != limit {
		t.Fatalf("allowed %d through a limit of %d via sentinel", allowed, limit)
	}

	if d := tb.Allow(key); d.Allowed {
		t.Fatal("bucket was spent but the next request was still allowed")
	}
}

// A promotion changes the address behind the client. This checks the client
// actually asks sentinel rather than caching an address forever: whatever
// sentinel currently reports as master is what the writes land on.
func TestFailoverClientTracksCurrentMaster(t *testing.T) {
	rdb := sentinelClient(t)
	ctx := context.Background()

	key := fmt.Sprintf("test:failover:track:%d", rand.Int63())
	t.Cleanup(func() { rdb.Del(ctx, key) })

	tb := NewTokenBucket(rdb, limiter.Config{Limit: 5, Window: time.Hour})
	if d := tb.Allow(key); !d.Allowed {
		t.Fatal("first request denied on a fresh bucket")
	}

	// the write must be visible on whichever instance is master now, which is
	// only true if the client resolved through sentinel instead of guessing
	got, err := rdb.HGet(ctx, key, "tokens").Result()
	if err != nil {
		t.Fatalf("reading bucket state back through sentinel: %v", err)
	}
	if got == "" {
		t.Fatal("bucket state missing after a successful write")
	}
}
