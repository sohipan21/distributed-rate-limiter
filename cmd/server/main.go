package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	ratelimitv1 "github.com/sohipan21/distributed-rate-limiter/gen/ratelimit/v1"
	"github.com/sohipan21/distributed-rate-limiter/internal/config"
	"github.com/sohipan21/distributed-rate-limiter/internal/grpcapi"
	"github.com/sohipan21/distributed-rate-limiter/internal/httpapi"
	"github.com/sohipan21/distributed-rate-limiter/internal/limiter"
	"github.com/sohipan21/distributed-rate-limiter/internal/metrics"
	"github.com/sohipan21/distributed-rate-limiter/internal/policy"
	"github.com/sohipan21/distributed-rate-limiter/internal/store"
)

// built-in fallback so the server runs with zero config; -config overrides
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
	degrade := flag.String("degrade", "open", "redis-down behavior: open (allow) or closed (deny)")
	configPath := flag.String("config", "", "yaml config file; empty uses built-in demo policies")
	flag.Parse()

	policies := demoPolicies()
	if *configPath != "" {
		cfg, err := config.Load(*configPath)
		if err != nil {
			log.Fatal(err)
		}
		policies = cfg.Policies
		log.Printf("policies loaded from %s", *configPath)
	}

	mode := store.FailOpen
	switch *degrade {
	case "open":
	case "closed":
		mode = store.FailClosed
	default:
		log.Fatalf("invalid -degrade %q (want open or closed)", *degrade)
	}

	mx := metrics.New()

	var m *policy.Manager
	if *redisAddr != "" {
		// short timeouts and no client retries bound worst-case decision
		// latency to one attempt; the breaker owns what happens when redis
		// is down, and stops paying even that once it's known-dead
		rdb := redis.NewClient(&redis.Options{
			Addr:         *redisAddr,
			DialTimeout:  300 * time.Millisecond,
			ReadTimeout:  300 * time.Millisecond,
			WriteTimeout: 300 * time.Millisecond,
			MaxRetries:   -1,
		})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := rdb.Ping(ctx).Err(); err != nil {
			// degradation exists so this isn't fatal: boot degraded, recover
			// when redis shows up
			log.Printf("redis unreachable at %s (%v), starting degraded", *redisAddr, err)
		}

		br := store.NewBreaker(3, time.Second)
		br.OnChange(func(degraded bool) {
			mx.SetDegraded(degraded)
			if degraded {
				mx.DegradationEvent()
				log.Printf("degraded: redis unreachable, failing %s", *degrade)
			} else {
				log.Print("recovered: redis reachable again")
			}
		})
		factory := store.Factory(rdb, store.WithMode(mode), store.WithBreaker(br), store.WithObserver(mx))
		m = policy.NewManagerWith(policies, factory, policy.WithObserver(mx))
		log.Printf("limiter state in redis at %s (fail-%s when unreachable)", *redisAddr, *degrade)
	} else {
		m = policy.NewManagerWith(policies, limiter.New, policy.WithObserver(mx))
		log.Print("limiter state in memory (single node only)")
	}

	// sighup reloads the config file without a restart; a bad edit keeps
	// the current policies and logs why
	if *configPath != "" {
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		go func() {
			for range hup {
				cfg, err := config.Load(*configPath)
				if err != nil {
					log.Printf("config reload failed, keeping current policies: %v", err)
					continue
				}
				m.SetPolicies(cfg.Policies)
				log.Printf("policies reloaded from %s", *configPath)
			}
		}()
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

	mux := http.NewServeMux()
	mux.Handle("/metrics", mx.Handler())
	mux.Handle("/", httpapi.Handler(m))

	log.Printf("http listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
