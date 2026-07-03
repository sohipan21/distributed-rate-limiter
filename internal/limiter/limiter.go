// Package limiter provides rate-limiting algorithms behind a single Limiter interface,
// so callers can swap algorithms (token bucket, sliding window) without changing how they check requests
package limiter

import "time"

// outcome of a single rate-limit check
type Decision struct {
	Allowed    bool
	Remaining  int
	RetryAfter time.Duration
	ResetAt    time.Time
}

// decides whether a request identified by key may proceed
type Limiter interface {
	Allow(key string) Decision
}

// describes a rate limit in one vocabulary shared by all algorithms:
// at most Limit events per Window
type Config struct {
	Limit  int
	Window time.Duration

	// optional token-bucket capacity override, zero means same as Limit;
	// sliding window ignores it
	Burst int
}

func (c Config) burst() int {
	if c.Burst > 0 {
		return c.Burst
	}
	return c.Limit
}
