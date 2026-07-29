package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	ratelimitv1 "github.com/sohipan21/distributed-rate-limiter/gen/ratelimit/v1"
	"github.com/sohipan21/distributed-rate-limiter/internal/auth"
	"github.com/sohipan21/distributed-rate-limiter/internal/config"
	"github.com/sohipan21/distributed-rate-limiter/internal/grpcapi"
	"github.com/sohipan21/distributed-rate-limiter/internal/httpapi"
	"github.com/sohipan21/distributed-rate-limiter/internal/limiter"
	"github.com/sohipan21/distributed-rate-limiter/internal/metrics"
	"github.com/sohipan21/distributed-rate-limiter/internal/policy"
	"github.com/sohipan21/distributed-rate-limiter/internal/store"
)

func toIdentities(keys map[string]config.APIKey) map[string]auth.Identity {
	if len(keys) == 0 {
		return nil
	}
	out := make(map[string]auth.Identity, len(keys))
	for k, v := range keys {
		out[k] = auth.Identity{Account: v.Account, Tier: v.Tier}
	}
	return out
}

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
	redisPool := flag.Int("redis-pool", 0, "redis connection pool size; 0 uses the client default (10 per CPU)")
	redisSentinel := flag.String("redis-sentinel", "", "comma-separated sentinel addresses; takes precedence over -redis")
	redisMaster := flag.String("redis-master-name", "mymaster", "sentinel master name, with -redis-sentinel")
	flag.Parse()

	policies := demoPolicies()
	var apiKeys map[string]auth.Identity
	if *configPath != "" {
		cfg, err := config.Load(*configPath)
		if err != nil {
			log.Fatal(err)
		}
		policies = cfg.Policies
		apiKeys = toIdentities(cfg.APIKeys)
		log.Printf("policies loaded from %s", *configPath)
	}

	// auth is on iff the config defines api keys; decided at boot
	var memAuth *auth.Memory
	var authn auth.Authenticator
	if len(apiKeys) > 0 {
		memAuth = auth.NewMemory(apiKeys)
		authn = memAuth
		log.Printf("auth on: %d api keys, identity and tier come from the key", len(apiKeys))
	} else {
		log.Print("auth off: identity and tier trusted from the client (demo mode)")
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
	if *redisAddr != "" || *redisSentinel != "" {
		// short timeouts and no client retries bound worst-case decision
		// latency to one attempt; the breaker owns what happens when redis
		// is down, and stops paying even that once it's known-dead
		//
		// 0 pool size leaves the go-redis default (10 per CPU). worth raising
		// under load: once every connection is busy, callers queue for one and
		// that wait lands in the decision latency, not in the redis timing
		var rdb redis.UniversalClient
		var redisDesc string
		if *redisSentinel != "" {
			// sentinel tracks which instance is master and the client follows
			// promotions, so a dead master costs a short blip rather than an
			// outage. see docs/tradeoffs.md for what that blip costs
			sentinels := strings.Split(*redisSentinel, ",")
			for i := range sentinels {
				sentinels[i] = strings.TrimSpace(sentinels[i])
			}
			rdb = redis.NewFailoverClient(&redis.FailoverOptions{
				MasterName:    *redisMaster,
				SentinelAddrs: sentinels,
				DialTimeout:   300 * time.Millisecond,
				ReadTimeout:   300 * time.Millisecond,
				WriteTimeout:  300 * time.Millisecond,
				MaxRetries:    -1,
				PoolSize:      *redisPool,
			})
			redisDesc = fmt.Sprintf("sentinel %v (master %q)", sentinels, *redisMaster)
		} else {
			rdb = redis.NewClient(&redis.Options{
				Addr:         *redisAddr,
				DialTimeout:  300 * time.Millisecond,
				ReadTimeout:  300 * time.Millisecond,
				WriteTimeout: 300 * time.Millisecond,
				MaxRetries:   -1,
				PoolSize:     *redisPool,
			})
			redisDesc = *redisAddr
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := rdb.Ping(ctx).Err(); err != nil {
			// degradation exists so this isn't fatal: boot degraded, recover
			// when redis shows up
			log.Printf("redis unreachable at %s (%v), starting degraded", redisDesc, err)
		}

		// redis-backed keys let ops add keys at runtime; the memory
		// backend stays first in the chain so config keys survive a
		// redis outage
		if memAuth != nil {
			ra := auth.NewRedis(rdb)
			if err := ra.Seed(ctx, apiKeys); err != nil {
				log.Printf("api key seed to redis failed (config keys still work): %v", err)
			}
			authn = auth.Chain{memAuth, ra}
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
		log.Printf("limiter state in redis at %s (fail-%s when unreachable)", redisDesc, *degrade)
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
				if memAuth != nil {
					memAuth.Set(toIdentities(cfg.APIKeys))
				}
				log.Printf("policies reloaded from %s", *configPath)
			}
		}()
	}

	if *grpcAddr != "" {
		lis, err := net.Listen("tcp", *grpcAddr)
		if err != nil {
			log.Fatalf("grpc listen: %v", err)
		}
		var grpcOpts []grpcapi.Option
		if authn != nil {
			grpcOpts = append(grpcOpts, grpcapi.WithAuth(authn))
		}
		srv := grpc.NewServer()
		ratelimitv1.RegisterRateLimiterServer(srv, grpcapi.NewServer(m, grpcOpts...))
		reflection.Register(srv) // lets grpcurl discover the service
		go func() { log.Fatal(srv.Serve(lis)) }()
		log.Printf("grpc listening on %s", *grpcAddr)
	}

	var httpOpts []httpapi.Option
	if authn != nil {
		httpOpts = append(httpOpts, httpapi.WithAuth(authn))
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", mx.Handler())
	mux.Handle("/", httpapi.Handler(m, httpOpts...))

	log.Printf("http listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
