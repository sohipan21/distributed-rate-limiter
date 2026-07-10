package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/sohipan21/distributed-rate-limiter/internal/httpapi"
	"github.com/sohipan21/distributed-rate-limiter/internal/limiter"
	"github.com/sohipan21/distributed-rate-limiter/internal/policy"
	"github.com/sohipan21/distributed-rate-limiter/internal/store"
)

// hardcoded demo policies until config loading arrives with the redis work
func demoPolicies() *policy.Policies {
	tb := func(n int) policy.Limit {
		return policy.Limit{
			Algorithm: limiter.TokenBucketAlgorithm,
			Config:    limiter.Config{Limit: n, Window: time.Minute},
		}
	}
	sw := func(n int) policy.Limit {
		return policy.Limit{
			Algorithm: limiter.SlidingWindowAlgorithm,
			Config:    limiter.Config{Limit: n, Window: time.Minute},
		}
	}

	p, err := policy.NewPolicies(
		tb(60),
		policy.Rule{Tier: "free", Limit: tb(10)},
		policy.Rule{Tier: "paid", Limit: tb(100)},
		policy.Rule{Endpoint: "/upload", Limit: sw(5)},
	)
	if err != nil {
		log.Fatal(err)
	}
	return p
}

// redis-backed limiters share counters across nodes; in-memory is per-process
func redisFactory(rdb *redis.Client) func(limiter.Algorithm, limiter.Config) limiter.Limiter {
	return func(a limiter.Algorithm, c limiter.Config) limiter.Limiter {
		if a == limiter.SlidingWindowAlgorithm {
			return store.NewSlidingWindow(rdb, c)
		}
		return store.NewTokenBucket(rdb, c)
	}
}

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	redisAddr := flag.String("redis", "", "redis address; empty runs in-memory limiters")
	flag.Parse()

	var m *policy.Manager
	if *redisAddr != "" {
		rdb := redis.NewClient(&redis.Options{Addr: *redisAddr})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := rdb.Ping(ctx).Err(); err != nil {
			log.Fatalf("redis unreachable at %s: %v", *redisAddr, err)
		}
		m = policy.NewManagerWith(demoPolicies(), redisFactory(rdb))
		log.Printf("limiter state in redis at %s", *redisAddr)
	} else {
		m = policy.NewManager(demoPolicies())
		log.Print("limiter state in memory (single node only)")
	}

	log.Printf("rate limiter listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, httpapi.Handler(m)))
}
