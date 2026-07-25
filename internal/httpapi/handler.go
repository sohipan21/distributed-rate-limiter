// Package httpapi exposes rate-limit decisions over HTTP with standard
// response semantics: 429 on deny plus X-RateLimit-* and Retry-After headers
package httpapi

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"

	"github.com/sohipan21/distributed-rate-limiter/internal/auth"
	"github.com/sohipan21/distributed-rate-limiter/internal/policy"
)

type options struct {
	auth auth.Authenticator
}

type Option func(*options)

// WithAuth turns on api-key auth: identity and tier come from the key
// lookup, and the client's own identity/tier fields are ignored
func WithAuth(a auth.Authenticator) Option {
	return func(o *options) { o.auth = a }
}

type checkRequest struct {
	Identity string `json:"identity"`
	Tier     string `json:"tier"`
	Endpoint string `json:"endpoint"`
}

type checkResponse struct {
	Allowed           bool  `json:"allowed"`
	Remaining         int   `json:"remaining"`
	RetryAfterSeconds int64 `json:"retry_after_seconds"`
	ResetAt           int64 `json:"reset_at"`
}

// POST /check: resolve the caller's limit, count the request, and answer
// with the decision
func Handler(m *policy.Manager, opts ...Option) http.Handler {
	var o options
	for _, fn := range opts {
		fn(&o)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /check", func(w http.ResponseWriter, r *http.Request) {
		var req checkRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}

		identity, tier := req.Identity, req.Tier
		if o.auth != nil {
			id, ok := o.auth.Lookup(r.Context(), r.Header.Get("X-API-Key"))
			if !ok {
				writeError(w, http.StatusUnauthorized, "invalid or missing api key")
				return
			}
			identity, tier = id.Account, id.Tier
		} else if identity == "" {
			writeError(w, http.StatusBadRequest, "identity is required")
			return
		}

		preq := policy.Request{Tier: tier, Endpoint: req.Endpoint}
		d := m.Allow(preq, identity)

		// Retry-After rounds up so clients never retry too early
		var retryAfter int64
		if !d.Allowed {
			retryAfter = int64(math.Ceil(d.RetryAfter.Seconds()))
			if retryAfter < 1 {
				retryAfter = 1
			}
		}

		// direct map writes: Header().Set would canonicalize the
		// conventional casing away (X-RateLimit -> X-Ratelimit)
		h := w.Header()
		h["X-RateLimit-Limit"] = []string{strconv.Itoa(m.Resolve(preq).Config.Limit)}
		h["X-RateLimit-Remaining"] = []string{strconv.Itoa(d.Remaining)}
		h["X-RateLimit-Reset"] = []string{strconv.FormatInt(d.ResetAt.Unix(), 10)}
		h.Set("Content-Type", "application/json")

		status := http.StatusOK
		if !d.Allowed {
			h.Set("Retry-After", strconv.FormatInt(retryAfter, 10))
			status = http.StatusTooManyRequests
		}
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(checkResponse{
			Allowed:           d.Allowed,
			Remaining:         d.Remaining,
			RetryAfterSeconds: retryAfter,
			ResetAt:           d.ResetAt.Unix(),
		})
	})
	return mux
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
