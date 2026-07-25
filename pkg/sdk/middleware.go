package sdk

import (
	"net/http"
	"strconv"
)

// pulls the limiter request out of an incoming http request
type KeyFunc func(*http.Request) Request

type config struct {
	keyFunc    KeyFunc
	failClosed bool
	onError    func(error)
}

type Option func(*config)

// WithKeyFunc overrides how a caller is identified. The default forwards the
// X-API-Key header (auth-on servers derive identity and tier from it), sets
// Identity to the key or the remote address for auth-off servers, and uses
// the URL path as the endpoint. Tier is never taken from the client.
func WithKeyFunc(fn KeyFunc) Option {
	return func(c *config) { c.keyFunc = fn }
}

// WithFailClosed denies requests when the limiter service is unreachable. The
// default fails open (allow), matching the service's own default.
func WithFailClosed() Option {
	return func(c *config) { c.failClosed = true }
}

// WithErrorHandler is called when a check fails (e.g. the service is down).
func WithErrorHandler(fn func(error)) Option {
	return func(c *config) { c.onError = fn }
}

// Middleware wraps a handler so every request is checked against the limiter.
// Denied requests get a 429 and never reach the wrapped handler.
func Middleware(checker Checker, opts ...Option) func(http.Handler) http.Handler {
	cfg := config{keyFunc: defaultKeyFunc}
	for _, fn := range opts {
		fn(&cfg)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			d, err := checker.Check(r.Context(), cfg.keyFunc(r))
			if err != nil {
				if cfg.onError != nil {
					cfg.onError(err)
				}
				if cfg.failClosed {
					// the limiter is down, not the caller's fault: 503, not 429
					http.Error(w, "rate limiter unavailable", http.StatusServiceUnavailable)
					return
				}
				next.ServeHTTP(w, r)
				return
			}

			setHeaders(w, d)
			if !d.Allowed {
				w.Header().Set("Retry-After", strconv.Itoa(int(d.RetryAfter.Seconds())))
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func defaultKeyFunc(r *http.Request) Request {
	key := r.Header.Get("X-API-Key")
	identity := key
	if identity == "" {
		identity = r.RemoteAddr
	}
	return Request{
		APIKey:   key,
		Identity: identity,
		Endpoint: r.URL.Path,
	}
}

// direct map writes: Header().Set canonicalizes the conventional casing away
// (X-RateLimit -> X-Ratelimit)
func setHeaders(w http.ResponseWriter, d Decision) {
	h := w.Header()
	h["X-RateLimit-Limit"] = []string{strconv.Itoa(d.Limit)}
	h["X-RateLimit-Remaining"] = []string{strconv.Itoa(d.Remaining)}
	h["X-RateLimit-Reset"] = []string{strconv.FormatInt(d.ResetAt.Unix(), 10)}
}
