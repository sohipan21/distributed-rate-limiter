package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sohipan21/distributed-rate-limiter/internal/policy"
)

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const valid = `
policies:
  default: {limit: 60, window: 1m}
  tiers:
    free: {limit: 10, window: 1m}
    paid: {limit: 100, window: 1m}
  endpoints:
    /upload: {limit: 5, window: 1m, algorithm: sliding_window}
api_keys:
  k_free: {account: alice, tier: free}
  k_paid: {account: bob, tier: paid}
`

func TestLoadValid(t *testing.T) {
	cfg, err := Load(write(t, valid))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	cases := []struct {
		req  policy.Request
		want int
	}{
		{policy.Request{Tier: "free", Endpoint: "/download"}, 10},
		{policy.Request{Tier: "paid", Endpoint: "/download"}, 100},
		{policy.Request{Tier: "free", Endpoint: "/upload"}, 5},
		{policy.Request{Tier: "unknown", Endpoint: "/x"}, 60},
	}
	for _, tc := range cases {
		if got := cfg.Policies.Resolve(tc.req).Config.Limit; got != tc.want {
			t.Errorf("Resolve(%+v) limit = %d, want %d", tc.req, got, tc.want)
		}
	}

	if k, ok := cfg.APIKeys["k_paid"]; !ok || k.Account != "bob" || k.Tier != "paid" {
		t.Errorf("k_paid = %+v, want bob/paid", k)
	}
}

func TestLoadRejectsBadConfig(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"not yaml", `{{{`},
		{"missing default", "policies:\n  tiers:\n    free: {limit: 10, window: 1m}\n"},
		{"zero limit", "policies:\n  default: {limit: 0, window: 1m}\n"},
		{"bad window", "policies:\n  default: {limit: 10, window: soon}\n"},
		{"unknown algorithm", "policies:\n  default: {limit: 10, window: 1m, algorithm: leaky_bucket}\n"},
	}
	for _, tc := range cases {
		if _, err := Load(write(t, tc.content)); err == nil {
			t.Errorf("%s: Load returned nil error", tc.name)
		}
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load("/nonexistent/config.yaml"); err == nil {
		t.Error("Load of missing file returned nil error")
	}
}
