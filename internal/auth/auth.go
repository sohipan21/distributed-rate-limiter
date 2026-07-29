// Package auth resolves api keys to accounts and tiers. the client presents
// a key; everything the limiter trusts (identity, tier) comes from the
// lookup, never from the request
package auth

import (
	"context"
	"sync/atomic"

	"github.com/redis/go-redis/v9"
)

// what a key resolves to
type Identity struct {
	Account string
	Tier    string
}

type Authenticator interface {
	Lookup(ctx context.Context, key string) (Identity, bool)
}

// Memory holds config-seeded keys; Set swaps the whole map on config reload
type Memory struct {
	keys atomic.Pointer[map[string]Identity]
}

func NewMemory(keys map[string]Identity) *Memory {
	m := &Memory{}
	m.Set(keys)
	return m
}

func (m *Memory) Set(keys map[string]Identity) {
	if keys == nil {
		keys = map[string]Identity{}
	}
	m.keys.Store(&keys)
}

func (m *Memory) Lookup(ctx context.Context, key string) (Identity, bool) {
	id, ok := (*m.keys.Load())[key]
	return id, ok
}

// Redis looks keys up in apikey:<key> hashes, so keys can be added at
// runtime without touching config. errors read as not-found; pair with a
// Memory backend in a Chain so config keys keep working through an outage
type Redis struct {
	rdb redis.UniversalClient
}

func NewRedis(rdb redis.UniversalClient) *Redis {
	return &Redis{rdb: rdb}
}

// Seed writes config-defined keys at boot so redis and config agree
func (r *Redis) Seed(ctx context.Context, keys map[string]Identity) error {
	for key, id := range keys {
		if err := r.rdb.HSet(ctx, "apikey:"+key, "account", id.Account, "tier", id.Tier).Err(); err != nil {
			return err
		}
	}
	return nil
}

func (r *Redis) Lookup(ctx context.Context, key string) (Identity, bool) {
	vals, err := r.rdb.HGetAll(ctx, "apikey:"+key).Result()
	if err != nil || vals["account"] == "" {
		return Identity{}, false
	}
	return Identity{Account: vals["account"], Tier: vals["tier"]}, true
}

// Chain tries each authenticator in order; first hit wins
type Chain []Authenticator

func (c Chain) Lookup(ctx context.Context, key string) (Identity, bool) {
	for _, a := range c {
		if id, ok := a.Lookup(ctx, key); ok {
			return id, true
		}
	}
	return Identity{}, false
}
