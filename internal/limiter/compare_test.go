package limiter

import (
	"testing"
	"time"
)

// advance both clocks, fire one request at each algorithm, pin what each should decide
type cmpStep struct {
	advance    time.Duration
	wantBucket bool
	wantWindow bool
}

func TestAlgorithmComparison(t *testing.T) {
	cases := []struct {
		name  string
		cfg   Config
		steps []cmpStep
	}{
		{
			name: "initial burst then deny",
			cfg:  Config{Limit: 3, Window: time.Second},
			steps: []cmpStep{
				{0, true, true},
				{0, true, true},
				{0, true, true},
				{0, false, false},
			},
		},
		{
			// one request per Window/Limit: both allow indefinitely
			name: "steady state at configured rate",
			cfg:  Config{Limit: 2, Window: time.Second},
			steps: []cmpStep{
				{0, true, true},
				{500 * time.Millisecond, true, true},
				{500 * time.Millisecond, true, true},
				{500 * time.Millisecond, true, true},
				{500 * time.Millisecond, true, true},
			},
		},
		{
			// where they diverge: token bucket refills mid-window, sliding
			// window denies until the burst ages out
			name: "recovery after burst diverges",
			cfg:  Config{Limit: 4, Window: time.Second},
			steps: []cmpStep{
				{0, true, true},
				{0, true, true},
				{0, true, true},
				{0, true, true},
				{500 * time.Millisecond, true, false},
				{0, true, false},
				{0, false, false},
				{500 * time.Millisecond, true, true}, // t=1s: burst aged out
			},
		},
		{
			name: "long idle recovery",
			cfg:  Config{Limit: 3, Window: time.Second},
			steps: []cmpStep{
				{0, true, true},
				{0, true, true},
				{0, true, true},
				{0, false, false},
				{time.Hour, true, true},
				{0, true, true},
				{0, true, true},
				{0, false, false},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tb, tbClock := newTestBucket(t, tc.cfg)
			sw, swClock := newTestWindow(t, tc.cfg)

			for i, s := range tc.steps {
				tbClock.Advance(s.advance)
				swClock.Advance(s.advance)

				if got := tb.Allow("k").Allowed; got != s.wantBucket {
					t.Errorf("step %d: token bucket allowed = %v, want %v", i+1, got, s.wantBucket)
				}
				if got := sw.Allow("k").Allowed; got != s.wantWindow {
					t.Errorf("step %d: sliding window allowed = %v, want %v", i+1, got, s.wantWindow)
				}
			}
		})
	}
}
