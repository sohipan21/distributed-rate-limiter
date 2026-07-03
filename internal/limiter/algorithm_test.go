package limiter

import (
	"testing"
	"time"
)

func TestNewReturnsConcreteTypes(t *testing.T) {
	cfg := Config{Limit: 1, Window: time.Second}

	if _, ok := New(TokenBucketAlgorithm, cfg).(*TokenBucket); !ok {
		t.Error("New(TokenBucketAlgorithm) did not return *TokenBucket")
	}
	if _, ok := New(SlidingWindowAlgorithm, cfg).(*SlidingWindow); !ok {
		t.Error("New(SlidingWindowAlgorithm) did not return *SlidingWindow")
	}
}

func TestNewLimitersEnforce(t *testing.T) {
	for _, algo := range []Algorithm{TokenBucketAlgorithm, SlidingWindowAlgorithm} {
		l := New(algo, Config{Limit: 1, Window: time.Minute})
		if d := l.Allow("k"); !d.Allowed {
			t.Errorf("%s: first request denied, want allowed", algo)
		}
		if d := l.Allow("k"); d.Allowed {
			t.Errorf("%s: second request allowed, want denied", algo)
		}
	}
}

func TestNewPanicsOnUnknownAlgorithm(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("New with unknown algorithm did not panic")
		}
	}()
	New("leaky_bucket", Config{Limit: 1, Window: time.Second})
}

func TestAlgorithmValid(t *testing.T) {
	for algo, want := range map[Algorithm]bool{
		TokenBucketAlgorithm:   true,
		SlidingWindowAlgorithm: true,
		"":                     false,
		"leaky_bucket":         false,
	} {
		if got := algo.Valid(); got != want {
			t.Errorf("Algorithm(%q).Valid() = %v, want %v", algo, got, want)
		}
	}
}

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		cfg     Config
		wantErr bool
	}{
		{Config{Limit: 1, Window: time.Second}, false},
		{Config{Limit: 10, Window: time.Minute, Burst: 20}, false},
		{Config{Limit: 0, Window: time.Second}, true},
		{Config{Limit: -1, Window: time.Second}, true},
		{Config{Limit: 1, Window: 0}, true},
		{Config{Limit: 1, Window: -time.Second}, true},
	}
	for _, tc := range cases {
		if err := tc.cfg.Validate(); (err != nil) != tc.wantErr {
			t.Errorf("Validate(%+v) error = %v, wantErr %v", tc.cfg, err, tc.wantErr)
		}
	}
}
