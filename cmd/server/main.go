package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	ratelimitv1 "github.com/sohipan21/distributed-rate-limiter/gen/ratelimit/v1"
	"github.com/sohipan21/distributed-rate-limiter/internal/grpcapi"
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

func main() {
	addr := flag.String("addr", ":8080", "http listen address")
	grpcAddr := flag.String("grpc", ":9090", "grpc listen address; empty disables grpc")
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
		m = policy.NewManagerWith(demoPolicies(), store.Factory(rdb))
		log.Printf("limiter state in redis at %s", *redisAddr)
	} else {
		m = policy.NewManager(demoPolicies())
		log.Print("limiter state in memory (single node only)")
	}

	if *grpcAddr != "" {
		lis, err := net.Listen("tcp", *grpcAddr)
		if err != nil {
			log.Fatalf("grpc listen: %v", err)
		}
		srv := grpc.NewServer()
		ratelimitv1.RegisterRateLimiterServer(srv, grpcapi.NewServer(m))
		reflection.Register(srv) // lets grpcurl discover the service
		go func() { log.Fatal(srv.Serve(lis)) }()
		log.Printf("grpc listening on %s", *grpcAddr)
	}

	log.Printf("http listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, httpapi.Handler(m)))
}
