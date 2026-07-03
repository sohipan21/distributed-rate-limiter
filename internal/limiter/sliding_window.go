package limiter

import (
	"sync"
	"time"
)

// in-memory sliding window log limiter, safe for concurrent use. each key
// keeps the timestamps of its last Window of requests; a request is allowed
// while fewer than Limit remain. exact — never more than Limit in any
// window-sized span — at the cost of one entry per request
type SlidingWindow struct {
	cfg Config

	mu      sync.Mutex
	windows map[string][]time.Time

	now func() time.Time // injectable for tests
}

// panics if Limit or Window is not positive — a config bug, not a runtime condition
func NewSlidingWindow(cfg Config) *SlidingWindow {
	if cfg.Limit <= 0 {
		panic("limiter: Config.Limit must be positive")
	}
	if cfg.Window <= 0 {
		panic("limiter: Config.Window must be positive")
	}
	return &SlidingWindow{
		cfg:     cfg,
		windows: make(map[string][]time.Time),
		now:     time.Now,
	}
}

func (sw *SlidingWindow) Allow(key string) Decision {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	now := sw.now()
	cutoff := now.Add(-sw.cfg.Window)

	// expired entries form a prefix; copy survivors down so the backing
	// array gets reused instead of growing forever
	w := sw.windows[key]
	i := 0
	for i < len(w) && !w[i].After(cutoff) {
		i++
	}
	if i > 0 {
		n := copy(w, w[i:])
		w = w[:n]
	}

	if len(w) >= sw.cfg.Limit {
		// a slot frees when the oldest entry ages out, full quota
		// returns when the newest does
		sw.windows[key] = w
		return Decision{
			RetryAfter: w[0].Add(sw.cfg.Window).Sub(now),
			ResetAt:    w[len(w)-1].Add(sw.cfg.Window),
		}
	}

	w = append(w, now)
	sw.windows[key] = w
	return Decision{
		Allowed:   true,
		Remaining: sw.cfg.Limit - len(w),
		ResetAt:   now.Add(sw.cfg.Window),
	}
}
