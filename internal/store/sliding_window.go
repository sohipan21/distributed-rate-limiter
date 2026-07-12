package store

import (
	"context"
	_ "embed"
	"math/rand/v2"

	"github.com/redis/go-redis/v9"

	"github.com/sohipan21/distributed-rate-limiter/internal/limiter"
)

//go:embed lua/sliding_window.lua
var slidingWindowScript string

// SlidingWindow is the redis-backed sliding window log, atomic via lua over
// a sorted set. mirrors the in-memory SlidingWindow, keyed in redis so nodes
// share one window
type SlidingWindow struct {
	rdb    *redis.Client
	cfg    limiter.Config
	script *redis.Script
	opts   options
}

func NewSlidingWindow(rdb *redis.Client, cfg limiter.Config, opts ...Option) *SlidingWindow {
	if err := cfg.Validate(); err != nil {
		panic(err)
	}
	sw := &SlidingWindow{rdb: rdb, cfg: cfg, script: redis.NewScript(slidingWindowScript)}
	for _, fn := range opts {
		fn(&sw.opts)
	}
	return sw
}

func (s *SlidingWindow) Allow(key string) limiter.Decision {
	if !s.opts.breaker.allow() {
		return degradedDecision(s.opts.mode)
	}
	// nonce disambiguates two requests landing in the same microsecond so
	// they don't collapse into one sorted-set member
	res, err := s.script.Run(context.Background(), s.rdb,
		[]string{key},
		s.cfg.Limit, s.cfg.Window.Microseconds(), rand.Int64(),
	).Int64Slice()
	if err != nil {
		s.opts.breaker.failure()
		return degradedDecision(s.opts.mode)
	}
	s.opts.breaker.success()
	return decode(res)
}
