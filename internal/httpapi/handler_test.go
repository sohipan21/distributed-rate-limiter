package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sohipan21/distributed-rate-limiter/internal/limiter"
	"github.com/sohipan21/distributed-rate-limiter/internal/policy"
)

func tb(n int) policy.Limit {
	return policy.Limit{
		Algorithm: limiter.TokenBucketAlgorithm,
		Config:    limiter.Config{Limit: n, Window: time.Minute},
	}
}

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	p, err := policy.NewPolicies(
		tb(10),
		policy.Rule{Tier: "free", Limit: tb(2)},
		policy.Rule{Tier: "paid", Limit: tb(5)},
		policy.Rule{Endpoint: "/upload", Limit: tb(3)},
	)
	if err != nil {
		t.Fatalf("NewPolicies: %v", err)
	}
	return Handler(policy.NewManager(p))
}

func check(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/check", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// exact map lookup: the handler writes X-RateLimit-* with conventional
// casing, which Header().Get's canonicalized lookup would miss
func hdr(w *httptest.ResponseRecorder, name string) string {
	if v := w.Header()[name]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func decode(t *testing.T, w *httptest.ResponseRecorder) checkResponse {
	t.Helper()
	var resp checkResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response %q: %v", w.Body.String(), err)
	}
	return resp
}

func TestCheckAllowed(t *testing.T) {
	h := testHandler(t)

	w := check(t, h, `{"identity":"alice","tier":"free","endpoint":"/download"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	resp := decode(t, w)
	if !resp.Allowed {
		t.Error("allowed = false, want true")
	}
	if resp.Remaining != 1 {
		t.Errorf("remaining = %d, want 1", resp.Remaining)
	}
	if got := hdr(w, "X-RateLimit-Limit"); got != "2" {
		t.Errorf("X-RateLimit-Limit = %q, want \"2\"", got)
	}
	if got := hdr(w, "X-RateLimit-Remaining"); got != "1" {
		t.Errorf("X-RateLimit-Remaining = %q, want \"1\"", got)
	}
	if w.Header().Get("Retry-After") != "" {
		t.Error("Retry-After set on an allowed response")
	}
}

func TestCheckDenied(t *testing.T) {
	h := testHandler(t)
	body := `{"identity":"alice","tier":"free","endpoint":"/download"}`

	check(t, h, body)
	check(t, h, body)
	w := check(t, h, body)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", w.Code)
	}
	resp := decode(t, w)
	if resp.Allowed {
		t.Error("allowed = true, want false")
	}
	if resp.Remaining != 0 {
		t.Errorf("remaining = %d, want 0", resp.Remaining)
	}
	ra, err := strconv.Atoi(w.Header().Get("Retry-After"))
	if err != nil || ra < 1 {
		t.Errorf("Retry-After = %q, want integer >= 1", w.Header().Get("Retry-After"))
	}
	if resp.RetryAfterSeconds != int64(ra) {
		t.Errorf("body retry_after_seconds = %d, header = %d", resp.RetryAfterSeconds, ra)
	}
}

func TestHeaderMath(t *testing.T) {
	h := testHandler(t)

	// free tier: 2 per minute. one consumed -> full again in ~30s
	before := time.Now()
	w := check(t, h, `{"identity":"alice","tier":"free","endpoint":"/download"}`)
	after := time.Now()

	reset, err := strconv.ParseInt(hdr(w, "X-RateLimit-Reset"), 10, 64)
	if err != nil {
		t.Fatalf("X-RateLimit-Reset = %q, want unix seconds", hdr(w, "X-RateLimit-Reset"))
	}
	lo := before.Add(29 * time.Second).Unix()
	hi := after.Add(31 * time.Second).Unix()
	if reset < lo || reset > hi {
		t.Errorf("X-RateLimit-Reset = %d, want within [%d, %d]", reset, lo, hi)
	}

	// exhaust, then Retry-After must be ceil(time to next token) <= 30s
	check(t, h, `{"identity":"alice","tier":"free","endpoint":"/download"}`)
	w = check(t, h, `{"identity":"alice","tier":"free","endpoint":"/download"}`)
	ra, _ := strconv.Atoi(w.Header().Get("Retry-After"))
	if ra < 1 || ra > 30 {
		t.Errorf("Retry-After = %d, want in [1, 30]", ra)
	}
}

func TestTierAndEndpointRouting(t *testing.T) {
	h := testHandler(t)

	// paid tier gets its larger limit through the full stack
	for i := 0; i < 5; i++ {
		w := check(t, h, `{"identity":"bob","tier":"paid","endpoint":"/download"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("paid request %d: status %d, want 200", i+1, w.Code)
		}
	}
	if w := check(t, h, `{"identity":"bob","tier":"paid","endpoint":"/download"}`); w.Code != http.StatusTooManyRequests {
		t.Errorf("paid request 6: status %d, want 429", w.Code)
	}

	// endpoint override applies regardless of tier
	for i := 0; i < 3; i++ {
		check(t, h, `{"identity":"carol","tier":"paid","endpoint":"/upload"}`)
	}
	w := check(t, h, `{"identity":"carol","tier":"paid","endpoint":"/upload"}`)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("upload request 4: status %d, want 429", w.Code)
	}
	if got := hdr(w, "X-RateLimit-Limit"); got != "3" {
		t.Errorf("X-RateLimit-Limit = %q, want \"3\" (endpoint override)", got)
	}
}

func TestHealthz(t *testing.T) {
	h := testHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "ok" {
		t.Errorf("healthz = %d %q, want 200 \"ok\"", w.Code, w.Body.String())
	}
}

func TestMethodNotAllowed(t *testing.T) {
	h := testHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/check", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /check status = %d, want 405", w.Code)
	}
}

func TestBadRequest(t *testing.T) {
	h := testHandler(t)

	if w := check(t, h, `{not json`); w.Code != http.StatusBadRequest {
		t.Errorf("invalid JSON status = %d, want 400", w.Code)
	}
	if w := check(t, h, `{"tier":"free","endpoint":"/download"}`); w.Code != http.StatusBadRequest {
		t.Errorf("missing identity status = %d, want 400", w.Code)
	}
}
