package store

import (
	"time"

	"github.com/sohipan21/distributed-rate-limiter/internal/limiter"
)

// what to do when redis can't answer: fail open keeps the service available
// (abuse protection pauses), fail closed keeps the guarantee (service 429s)
type Mode int

const (
	FailOpen Mode = iota
	FailClosed
)

type options struct {
	mode    Mode
	breaker *Breaker
}

type Option func(*options)

func WithMode(m Mode) Option {
	return func(o *options) { o.mode = m }
}

func WithBreaker(b *Breaker) Option {
	return func(o *options) { o.breaker = b }
}

// the decision handed out while degraded. client clock is fine here —
// redis is unreachable, so its clock is too. fail-open claims no remaining
// quota (it's unknowable); fail-closed's retry-after matches the breaker's
// probe cadence
func degradedDecision(m Mode) limiter.Decision {
	now := time.Now()
	if m == FailOpen {
		return limiter.Decision{Allowed: true, ResetAt: now}
	}
	return limiter.Decision{Allowed: false, RetryAfter: time.Second, ResetAt: now.Add(time.Second)}
}
