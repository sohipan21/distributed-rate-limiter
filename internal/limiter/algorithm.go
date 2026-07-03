package limiter

// names a rate-limiting algorithm; string-typed so values can come
// straight from config files later
type Algorithm string

const (
	TokenBucketAlgorithm   Algorithm = "token_bucket"
	SlidingWindowAlgorithm Algorithm = "sliding_window"
)

func (a Algorithm) Valid() bool {
	return a == TokenBucketAlgorithm || a == SlidingWindowAlgorithm
}

// builds the limiter for algo; panics on an unknown algorithm
func New(algo Algorithm, cfg Config) Limiter {
	switch algo {
	case TokenBucketAlgorithm:
		return NewTokenBucket(cfg)
	case SlidingWindowAlgorithm:
		return NewSlidingWindow(cfg)
	}
	panic("limiter: unknown algorithm " + string(algo))
}
