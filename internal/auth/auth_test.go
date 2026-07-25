package auth

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestMemoryLookup(t *testing.T) {
	m := NewMemory(map[string]Identity{"k1": {Account: "alice", Tier: "free"}})
	ctx := context.Background()

	if id, ok := m.Lookup(ctx, "k1"); !ok || id.Account != "alice" || id.Tier != "free" {
		t.Errorf("Lookup(k1) = %+v %v, want alice/free true", id, ok)
	}
	if _, ok := m.Lookup(ctx, "nope"); ok {
		t.Error("unknown key looked up as valid")
	}
	if _, ok := m.Lookup(ctx, ""); ok {
		t.Error("empty key looked up as valid")
	}
}

func TestMemorySetSwaps(t *testing.T) {
	m := NewMemory(map[string]Identity{"old": {Account: "a", Tier: "free"}})
	m.Set(map[string]Identity{"new": {Account: "b", Tier: "paid"}})
	ctx := context.Background()

	if _, ok := m.Lookup(ctx, "old"); ok {
		t.Error("old key survived Set")
	}
	if id, ok := m.Lookup(ctx, "new"); !ok || id.Tier != "paid" {
		t.Errorf("new key = %+v %v, want paid true", id, ok)
	}
}

func TestChainFirstHitWins(t *testing.T) {
	first := NewMemory(map[string]Identity{"k": {Account: "from-first", Tier: "free"}})
	second := NewMemory(map[string]Identity{
		"k":    {Account: "from-second", Tier: "paid"},
		"only": {Account: "second-only", Tier: "paid"},
	})
	c := Chain{first, second}
	ctx := context.Background()

	if id, _ := c.Lookup(ctx, "k"); id.Account != "from-first" {
		t.Errorf("chain returned %q, want from-first", id.Account)
	}
	if id, ok := c.Lookup(ctx, "only"); !ok || id.Account != "second-only" {
		t.Errorf("chain fallthrough = %+v %v, want second-only true", id, ok)
	}
	if _, ok := c.Lookup(ctx, "nowhere"); ok {
		t.Error("chain found a key no backend has")
	}
}

func testRedis(t *testing.T) *redis.Client {
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

func TestRedisSeedAndLookup(t *testing.T) {
	rdb := testRedis(t)
	r := NewRedis(rdb)
	ctx := context.Background()

	key := fmt.Sprintf("test-key-%d", rand.Int63())
	t.Cleanup(func() { rdb.Del(ctx, "apikey:"+key) })

	if err := r.Seed(ctx, map[string]Identity{key: {Account: "carol", Tier: "paid"}}); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if id, ok := r.Lookup(ctx, key); !ok || id.Account != "carol" || id.Tier != "paid" {
		t.Errorf("Lookup = %+v %v, want carol/paid true", id, ok)
	}
	if _, ok := r.Lookup(ctx, "missing-"+key); ok {
		t.Error("missing key looked up as valid")
	}
}
