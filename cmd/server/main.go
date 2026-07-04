package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/sohipan21/distributed-rate-limiter/internal/httpapi"
	"github.com/sohipan21/distributed-rate-limiter/internal/limiter"
	"github.com/sohipan21/distributed-rate-limiter/internal/policy"
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
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	m := policy.NewManager(demoPolicies())
	log.Printf("rate limiter listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, httpapi.Handler(m)))
}
