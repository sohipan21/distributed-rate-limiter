package policy

import (
	"sync"
	"time"

	"github.com/sohipan21/distributed-rate-limiter/internal/limiter"
)

// receives every decision; implemented by the metrics package so this
// package never imports prometheus
type Observer interface {
	ObserveDecision(tier, endpoint string, allowed bool, d time.Duration)
}

// enforces policies by keeping one live limiter per counting key,
// built lazily from the resolved limit. safe for concurrent use
type Manager struct {
	policies *Policies
	factory  limiter.Factory
	observer Observer

	mu       sync.Mutex
	limiters map[string]limiter.Limiter
}

type ManagerOption func(*Manager)

func WithObserver(o Observer) ManagerOption {
	return func(m *Manager) { m.observer = o }
}

func NewManager(p *Policies) *Manager {
	return NewManagerWith(p, limiter.New)
}

// NewManagerWith lets the caller pick where limiter state lives
// (in-memory, redis-backed)
func NewManagerWith(p *Policies, factory limiter.Factory, opts ...ManagerOption) *Manager {
	m := &Manager{
		policies: p,
		factory:  factory,
		limiters: make(map[string]limiter.Limiter),
	}
	for _, fn := range opts {
		fn(m)
	}
	return m
}

// the limit that would apply to req, without counting anything
func (m *Manager) Resolve(req Request) Limit {
	return m.policies.Resolve(req)
}

// resolves the limit for req and counts identity against it. endpoint-scoped
// rules count per identity+endpoint, everything else per identity alone;
// tier never enters the key since an identity implies its tier
func (m *Manager) Allow(req Request, identity string) limiter.Decision {
	lim, endpointScoped := m.policies.match(req)

	key := identity
	if endpointScoped {
		key = identity + "|" + req.Endpoint
	}

	m.mu.Lock()
	l, ok := m.limiters[key]
	if !ok {
		l = m.factory(lim.Algorithm, lim.Config)
		m.limiters[key] = l
	}
	m.mu.Unlock()

	if m.observer == nil {
		return l.Allow(key)
	}
	start := time.Now()
	d := l.Allow(key)
	m.observer.ObserveDecision(req.Tier, req.Endpoint, d.Allowed, time.Since(start))
	return d
}
