package metrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestObserveDecision(t *testing.T) {
	m := New()
	m.ObserveDecision("free", "/download", true, time.Millisecond)
	m.ObserveDecision("free", "/download", true, time.Millisecond)
	m.ObserveDecision("free", "/download", false, time.Millisecond)

	if got := testutil.ToFloat64(m.decisions.WithLabelValues("free", "/download", "allowed")); got != 2 {
		t.Errorf("allowed counter = %v, want 2", got)
	}
	if got := testutil.ToFloat64(m.decisions.WithLabelValues("free", "/download", "denied")); got != 1 {
		t.Errorf("denied counter = %v, want 1", got)
	}
	if got := sampleCount(t, m, "ratelimiter_decision_duration_seconds"); got != 3 {
		t.Errorf("duration samples = %d, want 3", got)
	}
}

func TestObserveRedis(t *testing.T) {
	m := New()
	m.ObserveRedis("token_bucket", time.Millisecond, true)
	m.ObserveRedis("token_bucket", time.Millisecond, false)

	if got := sampleCount(t, m, "ratelimiter_redis_duration_seconds"); got != 2 {
		t.Errorf("redis duration samples = %d, want 2", got)
	}
	if got := testutil.ToFloat64(m.redisErrors); got != 1 {
		t.Errorf("redis errors = %v, want 1", got)
	}
}

func TestDegradation(t *testing.T) {
	m := New()
	m.SetDegraded(true)
	m.DegradationEvent()
	if got := testutil.ToFloat64(m.degraded); got != 1 {
		t.Errorf("degraded gauge = %v, want 1", got)
	}
	m.SetDegraded(false)
	if got := testutil.ToFloat64(m.degraded); got != 0 {
		t.Errorf("degraded gauge = %v, want 0", got)
	}
	if got := testutil.ToFloat64(m.degradationEvents); got != 1 {
		t.Errorf("degradation events = %v, want 1", got)
	}
}

func TestHandlerServesMetrics(t *testing.T) {
	m := New()
	m.ObserveDecision("free", "/x", true, time.Millisecond)

	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(w.Result().Body)

	for _, name := range []string{
		"ratelimiter_decisions_total",
		"ratelimiter_decision_duration_seconds",
		"ratelimiter_degraded",
		"go_goroutines",
	} {
		if !strings.Contains(string(body), name) {
			t.Errorf("metrics output missing %s", name)
		}
	}
}

// total sample count across all series of a histogram family
func sampleCount(t *testing.T, m *Metrics, name string) uint64 {
	t.Helper()
	families, err := m.reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var n uint64
	for _, f := range families {
		if f.GetName() == name {
			for _, mtr := range f.GetMetric() {
				n += mtr.GetHistogram().GetSampleCount()
			}
		}
	}
	return n
}
