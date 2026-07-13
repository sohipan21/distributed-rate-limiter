// Package metrics owns the prometheus collectors and implements the
// observer hooks the policy and store packages expose, so those packages
// never import prometheus themselves
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// sub-ms buckets: day-9 baselines put decisions at ~0.4ms in memory and
// ~1-4ms against redis
var buckets = []float64{0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1}

type Metrics struct {
	reg *prometheus.Registry

	decisions         *prometheus.CounterVec
	decisionDuration  prometheus.Histogram
	redisDuration     *prometheus.HistogramVec
	redisErrors       prometheus.Counter
	degraded          prometheus.Gauge
	degradationEvents prometheus.Counter
}

// New builds all collectors on a private registry, so tests can create
// as many instances as they want without duplicate-registration panics
func New() *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	f := promauto.With(reg)

	return &Metrics{
		reg: reg,
		decisions: f.NewCounterVec(prometheus.CounterOpts{
			Name: "ratelimiter_decisions_total",
			Help: "rate-limit decisions by tier, endpoint, and outcome",
		}, []string{"tier", "endpoint", "decision"}),
		decisionDuration: f.NewHistogram(prometheus.HistogramOpts{
			Name:    "ratelimiter_decision_duration_seconds",
			Help:    "time to make a rate-limit decision",
			Buckets: buckets,
		}),
		redisDuration: f.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "ratelimiter_redis_duration_seconds",
			Help:    "redis script round-trip time",
			Buckets: buckets,
		}, []string{"algorithm"}),
		redisErrors: f.NewCounter(prometheus.CounterOpts{
			Name: "ratelimiter_redis_errors_total",
			Help: "failed redis round trips",
		}),
		degraded: f.NewGauge(prometheus.GaugeOpts{
			Name: "ratelimiter_degraded",
			Help: "1 while the breaker is open and redis is being skipped",
		}),
		degradationEvents: f.NewCounter(prometheus.CounterOpts{
			Name: "ratelimiter_degradation_events_total",
			Help: "times the breaker opened",
		}),
	}
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// implements policy.Observer
func (m *Metrics) ObserveDecision(tier, endpoint string, allowed bool, d time.Duration) {
	decision := "denied"
	if allowed {
		decision = "allowed"
	}
	m.decisions.WithLabelValues(tier, endpoint, decision).Inc()
	m.decisionDuration.Observe(d.Seconds())
}

// implements store.Observer
func (m *Metrics) ObserveRedis(algorithm string, d time.Duration, ok bool) {
	m.redisDuration.WithLabelValues(algorithm).Observe(d.Seconds())
	if !ok {
		m.redisErrors.Inc()
	}
}

func (m *Metrics) SetDegraded(on bool) {
	if on {
		m.degraded.Set(1)
	} else {
		m.degraded.Set(0)
	}
}

func (m *Metrics) DegradationEvent() {
	m.degradationEvents.Inc()
}
