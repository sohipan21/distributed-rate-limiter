// Package limiter provides rate-limiting algorithms behind a single Limiter, interface
// so callers can swap algoriths (token bucket, sliding window) without changing how they check requests
package limiter

import "time"

// outcome of a single rate-limit check
type Decision struct {
	Allowed    bool          //allowed reports whether the request was permitted
	Remaining  int           //quota left for the key after this decision
	RetryAfter time.Duration //how long caller should wait before retrying, zero if allowed
	ResetAt    time.Time     //time when the key's quota returns to its full capacity
}

// decides whether a request identified by key may proceed
type Limiter interface {
	Allow(key string) Decision
}

// describes a rate limit in one vocabulary shared by all algorithms
type Config struct {
	Limit  int           //max num of events per window, positive
	Window time.Duration //duration of the rate limit window, positive

	// Burst optionally overrides the token-bucket capacity, allowing a
	// short burst larger or smaller than Limit. Zero means Burst == Limit.
	// Sliding window ignores it: the window itself bounds bursts.
	Burst int
}

// returns the effective burst capacity
func (c Config) burst() int {
	if c.Burst > 0 {
		return c.Burst
	}
	return c.Limit
}
