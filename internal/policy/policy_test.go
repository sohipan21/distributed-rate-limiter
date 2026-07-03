package policy

import (
	"math/rand"
	"testing"
	"time"

	"github.com/sohipan21/distributed-rate-limiter/internal/limiter"
)

// limits in tests are told apart by their Limit value
func lim(n int) Limit {
	return Limit{
		Algorithm: limiter.TokenBucketAlgorithm,
		Config:    limiter.Config{Limit: n, Window: time.Minute},
	}
}

func testRules() []Rule {
	return []Rule{
		{Tier: "free", Limit: lim(2)},
		{Tier: "paid", Limit: lim(3)},
		{Endpoint: "/upload", Limit: lim(4)},
		{Tier: "paid", Endpoint: "/upload", Limit: lim(5)},
	}
}

func TestResolvePrecedence(t *testing.T) {
	p, err := NewPolicies(lim(1), testRules()...)
	if err != nil {
		t.Fatalf("NewPolicies: %v", err)
	}

	cases := []struct {
		name string
		req  Request
		want int
	}{
		{"tier rule", Request{Tier: "free", Endpoint: "/download"}, 2},
		{"other tier rule", Request{Tier: "paid", Endpoint: "/download"}, 3},
		{"endpoint override beats tier", Request{Tier: "free", Endpoint: "/upload"}, 4},
		{"exact tier+endpoint beats both", Request{Tier: "paid", Endpoint: "/upload"}, 5},
		{"endpoint rule without tier", Request{Endpoint: "/upload"}, 4},
		{"default when nothing matches", Request{Tier: "enterprise", Endpoint: "/status"}, 1},
		{"default on empty request", Request{}, 1},
	}
	for _, tc := range cases {
		if got := p.Resolve(tc.req).Config.Limit; got != tc.want {
			t.Errorf("%s: Resolve(%+v) limit = %d, want %d", tc.name, tc.req, got, tc.want)
		}
	}
}

func TestResolveOrderIndependence(t *testing.T) {
	requests := []Request{
		{Tier: "free", Endpoint: "/download"},
		{Tier: "paid", Endpoint: "/download"},
		{Tier: "free", Endpoint: "/upload"},
		{Tier: "paid", Endpoint: "/upload"},
		{Tier: "enterprise", Endpoint: "/status"},
	}

	base, err := NewPolicies(lim(1), testRules()...)
	if err != nil {
		t.Fatalf("NewPolicies: %v", err)
	}
	want := make([]int, len(requests))
	for i, req := range requests {
		want[i] = base.Resolve(req).Config.Limit
	}

	// same rules in shuffled registration orders must resolve identically
	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 50; trial++ {
		rules := testRules()
		rng.Shuffle(len(rules), func(i, j int) { rules[i], rules[j] = rules[j], rules[i] })

		p, err := NewPolicies(lim(1), rules...)
		if err != nil {
			t.Fatalf("trial %d: NewPolicies: %v", trial, err)
		}
		for i, req := range requests {
			if got := p.Resolve(req).Config.Limit; got != want[i] {
				t.Fatalf("trial %d: Resolve(%+v) limit = %d, want %d", trial, req, got, want[i])
			}
		}
	}
}

func TestNewPoliciesErrors(t *testing.T) {
	bad := Limit{Algorithm: limiter.TokenBucketAlgorithm, Config: limiter.Config{Limit: 0, Window: time.Minute}}

	cases := []struct {
		name  string
		def   Limit
		rules []Rule
	}{
		{"bad default config", bad, nil},
		{"bad rule config", lim(1), []Rule{{Tier: "free", Limit: bad}}},
		{"unknown algorithm", lim(1), []Rule{{Tier: "free", Limit: Limit{Algorithm: "leaky_bucket", Config: limiter.Config{Limit: 1, Window: time.Minute}}}}},
		{"duplicate matcher", lim(1), []Rule{{Tier: "free", Limit: lim(2)}, {Tier: "free", Limit: lim(3)}}},
	}
	for _, tc := range cases {
		if _, err := NewPolicies(tc.def, tc.rules...); err == nil {
			t.Errorf("%s: NewPolicies returned nil error", tc.name)
		}
	}
}
