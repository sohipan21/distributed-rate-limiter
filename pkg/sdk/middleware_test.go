package sdk

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type fakeChecker struct {
	decision Decision
	err      error
	lastReq  Request
}

func (f *fakeChecker) Check(ctx context.Context, req Request) (Decision, error) {
	f.lastReq = req
	return f.decision, f.err
}

// exact-case header lookup: the middleware writes X-RateLimit-* with
// conventional casing that Header().Get would canonicalize past
func hdr(w *httptest.ResponseRecorder, name string) string {
	if v := w.Header()[name]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func run(t *testing.T, c Checker, opts ...Option) (*httptest.ResponseRecorder, *bool) {
	t.Helper()
	nextRan := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextRan = true
		w.WriteHeader(http.StatusOK)
	})
	h := Middleware(c, opts...)(next)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/download", nil))
	return w, &nextRan
}

func TestAllowedPassesThrough(t *testing.T) {
	c := &fakeChecker{decision: Decision{Allowed: true, Limit: 10, Remaining: 9, ResetAt: time.Unix(1783732312, 0)}}
	w, nextRan := run(t, c)

	if w.Code != http.StatusOK || !*nextRan {
		t.Fatalf("status=%d nextRan=%v, want 200 and true", w.Code, *nextRan)
	}
	if got := hdr(w, "X-RateLimit-Remaining"); got != "9" {
		t.Errorf("X-RateLimit-Remaining = %q, want \"9\"", got)
	}
	if got := hdr(w, "X-RateLimit-Limit"); got != "10" {
		t.Errorf("X-RateLimit-Limit = %q, want \"10\"", got)
	}
}

func TestDeniedReturns429(t *testing.T) {
	c := &fakeChecker{decision: Decision{Allowed: false, Remaining: 0, RetryAfter: 5 * time.Second}}
	w, nextRan := run(t, c)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", w.Code)
	}
	if *nextRan {
		t.Error("wrapped handler ran on a denied request")
	}
	if got := w.Header().Get("Retry-After"); got != "5" {
		t.Errorf("Retry-After = %q, want \"5\"", got)
	}
}

func TestLimiterErrorFailsOpenByDefault(t *testing.T) {
	c := &fakeChecker{err: errors.New("service down")}
	w, nextRan := run(t, c)

	if w.Code != http.StatusOK || !*nextRan {
		t.Errorf("status=%d nextRan=%v, want 200 and true (fail open)", w.Code, *nextRan)
	}
}

func TestLimiterErrorFailClosed(t *testing.T) {
	c := &fakeChecker{err: errors.New("service down")}
	var gotErr error
	w, nextRan := run(t, c, WithFailClosed(), WithErrorHandler(func(e error) { gotErr = e }))

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
	if *nextRan {
		t.Error("wrapped handler ran while failing closed")
	}
	if gotErr == nil {
		t.Error("error handler was not called")
	}
}

func TestCustomKeyFunc(t *testing.T) {
	c := &fakeChecker{decision: Decision{Allowed: true}}
	run(t, c, WithKeyFunc(func(r *http.Request) Request {
		return Request{Identity: "custom", Tier: "paid", Endpoint: "/x"}
	}))

	if c.lastReq.Identity != "custom" || c.lastReq.Tier != "paid" || c.lastReq.Endpoint != "/x" {
		t.Errorf("checker saw %+v, want custom/paid/x", c.lastReq)
	}
}

func TestDefaultKeyFunc(t *testing.T) {
	c := &fakeChecker{decision: Decision{Allowed: true}}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	h := Middleware(c)(next)

	req := httptest.NewRequest(http.MethodGet, "/upload", nil)
	req.Header.Set("X-API-Key", "alice")
	req.Header.Set("X-Tier", "free")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if c.lastReq.Identity != "alice" || c.lastReq.Tier != "free" || c.lastReq.Endpoint != "/upload" {
		t.Errorf("default key func produced %+v, want alice/free/upload", c.lastReq)
	}
}

func TestDefaultKeyFuncFallsBackToHost(t *testing.T) {
	c := &fakeChecker{decision: Decision{Allowed: true}}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	Middleware(c)(next).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

	if c.lastReq.Identity == "" {
		t.Error("identity empty with no X-API-Key, want RemoteAddr fallback")
	}
}
