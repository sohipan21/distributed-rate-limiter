package policy

import (
	"sync"

	"github.com/sohipan21/distributed-rate-limiter/internal/limiter"
)

// enforces policies by keeping one live limiter per counting key,
// built lazily from the resolved limit. safe for concurrent use
type Manager struct {
	policies *Policies
	factory  func(limiter.Algorithm, limiter.Config) limiter.Limiter

	mu       sync.Mutex
	limiters map[string]limiter.Limiter
}

func NewManager(p *Policies) *Manager {
	return NewManagerWith(p, limiter.New)
}

// NewManagerWith lets the caller pick where limiter state lives
// (in-memory, redis-backed)
func NewManagerWith(p *Policies, factory func(limiter.Algorithm, limiter.Config) limiter.Limiter) *Manager {
	return &Manager{
		policies: p,
		factory:  factory,
		limiters: make(map[string]limiter.Limiter),
	}
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

	return l.Allow(key)
}
