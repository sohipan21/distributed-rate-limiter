package store

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/sohipan21/distributed-rate-limiter/internal/limiter"
)

// redis-backed tests need a live redis (make up); they skip when it's not there
func testClient(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("redis not reachable at %s: %v", addr, err)
	}
	t.Cleanup(func() { rdb.Close() })
	return rdb
}

func testKey(t *testing.T, rdb *redis.Client) string {
	t.Helper()
	key := fmt.Sprintf("test:naive:%d", rand.Int63())
	t.Cleanup(func() { rdb.Del(context.Background(), key) })
	return key
}

// fire workers*perWorker requests at one key, count how many get through
func hammer(l limiter.Limiter, key string, workers, perWorker int) int {
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				if l.Allow(key).Allowed {
					mu.Lock()
					allowed++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	return allowed
}

func TestNaiveBucketSequential(t *testing.T) {
	rdb := testClient(t)
	nb := NewNaiveTokenBucket(rdb, limiter.Config{Limit: 5, Window: time.Hour})
	key := testKey(t, rdb)

	// single-threaded the naive store behaves — that's what makes it sneaky
	for i := 0; i < 5; i++ {
		d := nb.Allow(key)
		if !d.Allowed {
			t.Fatalf("request %d: denied, want allowed", i+1)
		}
		if want := 5 - (i + 1); d.Remaining != want {
			t.Errorf("request %d: Remaining = %d, want %d", i+1, d.Remaining, want)
		}
	}
	if d := nb.Allow(key); d.Allowed {
		t.Fatal("request 6: allowed, want denied")
	}
}

func TestNaiveBucketOvercountsUnderConcurrency(t *testing.T) {
	rdb := testClient(t)
	const limit = 100
	nb := NewNaiveTokenBucket(rdb, limiter.Config{Limit: limit, Window: time.Hour})

	// read-modify-write over two round trips loses updates under concurrency,
	// so more than limit requests get through. a few rounds in case one run
	// interleaves luckily; in practice the first round overcounts every time
	for round := 1; round <= 5; round++ {
		key := testKey(t, rdb)
		allowed := hammer(nb, key, 50, 10)
		if allowed > limit {
			t.Logf("round %d: naive store allowed %d of limit %d (overcount %d, %d requests total)",
				round, allowed, limit, allowed-limit, 50*10)
			return
		}
		t.Logf("round %d: no overcount (allowed %d), retrying", round, allowed)
	}
	t.Fatal("naive store never overcounted in 5 rounds — race not reproduced")
}
