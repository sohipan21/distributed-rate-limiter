package store

import (
	"sync"
	"time"
)

// Breaker trips after consecutive redis failures so a dead redis stops
// costing a timeout per request. while open, limiters skip redis and hand
// out degraded decisions; after cooldown one probe request goes through —
// success closes the breaker, failure buys another cooldown. one breaker is
// shared by every limiter built from the same factory, so the first limiter
// to notice a dead redis flips them all
type Breaker struct {
	threshold int
	cooldown  time.Duration
	onChange  func(degraded bool)

	mu        sync.Mutex
	failures  int
	open      bool
	probing   bool
	openUntil time.Time

	now func() time.Time // injectable for tests
}

func NewBreaker(threshold int, cooldown time.Duration) *Breaker {
	return &Breaker{threshold: threshold, cooldown: cooldown, now: time.Now}
}

// OnChange registers a callback fired once per open/close transition.
// set it before the breaker sees traffic
func (b *Breaker) OnChange(fn func(degraded bool)) {
	b.onChange = fn
}

func (b *Breaker) Degraded() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.open
}

// reports whether redis should be tried at all. a nil breaker always says yes
func (b *Breaker) allow() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.open {
		return true
	}
	// one probe at a time once the cooldown is up
	if !b.probing && !b.now().Before(b.openUntil) {
		b.probing = true
		return true
	}
	return false
}

func (b *Breaker) success() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.failures = 0
	b.probing = false
	fire := b.open
	b.open = false
	b.mu.Unlock()

	if fire && b.onChange != nil {
		b.onChange(false)
	}
}

func (b *Breaker) failure() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.probing = false
	b.failures++
	fire := false
	if b.failures >= b.threshold {
		if !b.open {
			fire = true
		}
		b.open = true
		b.openUntil = b.now().Add(b.cooldown)
	}
	b.mu.Unlock()

	if fire && b.onChange != nil {
		b.onChange(true)
	}
}
