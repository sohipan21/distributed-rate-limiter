package policy

import (
	"sync"
	"testing"
	"time"

	"github.com/sohipan21/distributed-rate-limiter/internal/limiter"
)

// windows are long so refill/expiry never fires mid-test; time-based
// correctness is covered in the limiter package
func testManager(t *testing.T) *Manager {
	t.Helper()
	p, err := NewPolicies(
		lim(10),
		Rule{Tier: "free", Limit: lim(2)},
		Rule{Tier: "paid", Limit: lim(5)},
		Rule{Endpoint: "/upload", Limit: lim(3)},
	)
	if err != nil {
		t.Fatalf("NewPolicies: %v", err)
	}
	return NewManager(p)
}

func TestManagerEnforcesResolvedLimits(t *testing.T) {
	m := testManager(t)
	free := Request{Tier: "free", Endpoint: "/download"}
	paid := Request{Tier: "paid", Endpoint: "/download"}

	for i := 0; i < 2; i++ {
		if d := m.Allow(free, "alice"); !d.Allowed {
			t.Fatalf("free request %d: denied, want allowed", i+1)
		}
	}
	if d := m.Allow(free, "alice"); d.Allowed {
		t.Fatal("free request 3: allowed, want denied")
	}

	// paid tier resolves to its own larger limit
	for i := 0; i < 5; i++ {
		if d := m.Allow(paid, "bob"); !d.Allowed {
			t.Fatalf("paid request %d: denied, want allowed", i+1)
		}
	}
	if d := m.Allow(paid, "bob"); d.Allowed {
		t.Fatal("paid request 6: allowed, want denied")
	}
}

func TestManagerPerIdentityIsolation(t *testing.T) {
	m := testManager(t)
	free := Request{Tier: "free", Endpoint: "/download"}

	m.Allow(free, "alice")
	m.Allow(free, "alice")
	if d := m.Allow(free, "alice"); d.Allowed {
		t.Fatal("alice exhausted but allowed")
	}
	if d := m.Allow(free, "bob"); !d.Allowed {
		t.Fatal("bob denied by alice's exhausted quota")
	}
}

func TestManagerEndpointScopedCounters(t *testing.T) {
	m := testManager(t)
	upload := Request{Tier: "free", Endpoint: "/upload"}
	download := Request{Tier: "free", Endpoint: "/download"}

	// /upload has an endpoint override (limit 3) counted separately
	for i := 0; i < 3; i++ {
		if d := m.Allow(upload, "alice"); !d.Allowed {
			t.Fatalf("upload request %d: denied, want allowed", i+1)
		}
	}
	if d := m.Allow(upload, "alice"); d.Allowed {
		t.Fatal("upload request 4: allowed, want denied")
	}

	// /download draws the tier-wide counter, untouched by /upload traffic
	if d := m.Allow(download, "alice"); !d.Allowed {
		t.Fatal("download denied, want allowed (separate counter)")
	}
}

func TestManagerTierLimitSharedAcrossEndpoints(t *testing.T) {
	m := testManager(t)

	// free tier (limit 2) has no endpoint constraint: one counter for both
	if d := m.Allow(Request{Tier: "free", Endpoint: "/a"}, "alice"); !d.Allowed {
		t.Fatal("first request denied")
	}
	if d := m.Allow(Request{Tier: "free", Endpoint: "/b"}, "alice"); !d.Allowed {
		t.Fatal("second request denied")
	}
	if d := m.Allow(Request{Tier: "free", Endpoint: "/c"}, "alice"); d.Allowed {
		t.Fatal("third request allowed, want denied (shared tier counter)")
	}
}

func TestManagerReusesCachedLimiter(t *testing.T) {
	m := testManager(t)
	paid := Request{Tier: "paid", Endpoint: "/download"}

	for want := 4; want >= 0; want-- {
		d := m.Allow(paid, "alice")
		if !d.Allowed {
			t.Fatalf("request denied with %d expected remaining", want)
		}
		if d.Remaining != want {
			t.Fatalf("Remaining = %d, want %d (limiter not reused?)", d.Remaining, want)
		}
	}
}

func TestManagerSetPoliciesAppliesNewLimits(t *testing.T) {
	p1, err := NewPolicies(lim(2))
	if err != nil {
		t.Fatalf("NewPolicies: %v", err)
	}
	m := NewManager(p1)
	req := Request{Tier: "free", Endpoint: "/x"}

	m.Allow(req, "alice")
	m.Allow(req, "alice")
	if m.Allow(req, "alice").Allowed {
		t.Fatal("third request allowed under limit 2")
	}

	// reload with a bigger limit: takes effect immediately, counters reset
	p2, err := NewPolicies(lim(5))
	if err != nil {
		t.Fatalf("NewPolicies: %v", err)
	}
	m.SetPolicies(p2)

	if got := m.Resolve(req).Config.Limit; got != 5 {
		t.Errorf("Resolve after reload = %d, want 5", got)
	}
	d := m.Allow(req, "alice")
	if !d.Allowed || d.Remaining != 4 {
		t.Errorf("post-reload decision = %+v, want allowed with 4 remaining (fresh limiter)", d)
	}
}

func TestManagerConcurrentExactLimit(t *testing.T) {
	p, err := NewPolicies(lim(100))
	if err != nil {
		t.Fatalf("NewPolicies: %v", err)
	}
	m := NewManager(p)
	req := Request{Tier: "free", Endpoint: "/x"}

	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if m.Allow(req, "alice").Allowed {
					mu.Lock()
					allowed++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	if allowed != 100 {
		t.Errorf("allowed %d of 500 concurrent requests, want exactly 100", allowed)
	}
}

type decisionRecord struct {
	tier, endpoint string
	allowed        bool
	d              time.Duration
}

type fakeObserver struct{ records []decisionRecord }

func (f *fakeObserver) ObserveDecision(tier, endpoint string, allowed bool, d time.Duration) {
	f.records = append(f.records, decisionRecord{tier, endpoint, allowed, d})
}

func TestManagerReportsToObserver(t *testing.T) {
	p, err := NewPolicies(lim(1))
	if err != nil {
		t.Fatalf("NewPolicies: %v", err)
	}
	fo := &fakeObserver{}
	m := NewManagerWith(p, limiter.New, WithObserver(fo))

	req := Request{Tier: "free", Endpoint: "/x"}
	m.Allow(req, "alice") // allowed
	m.Allow(req, "alice") // denied, limit 1

	if len(fo.records) != 2 {
		t.Fatalf("observer saw %d decisions, want 2", len(fo.records))
	}
	first, second := fo.records[0], fo.records[1]
	if first.tier != "free" || first.endpoint != "/x" {
		t.Errorf("labels = %q %q, want free /x", first.tier, first.endpoint)
	}
	if !first.allowed || second.allowed {
		t.Errorf("allowed sequence = %v, %v; want true, false", first.allowed, second.allowed)
	}
}

func TestManagerUsesInjectedFactory(t *testing.T) {
	p, err := NewPolicies(lim(2))
	if err != nil {
		t.Fatalf("NewPolicies: %v", err)
	}
	calls := 0
	m := NewManagerWith(p, func(a limiter.Algorithm, c limiter.Config) limiter.Limiter {
		calls++
		return limiter.New(a, c)
	})

	m.Allow(Request{}, "alice")
	m.Allow(Request{}, "alice") // cached, no new construction
	m.Allow(Request{}, "bob")

	if calls != 2 {
		t.Errorf("factory calls = %d, want 2 (one per distinct key)", calls)
	}
}

// guard against limits sneaking into the shared counter via config drift:
// two identities on the same rule get their own limiter instances
func TestManagerSeparateLimiterPerIdentity(t *testing.T) {
	m := testManager(t)
	free := Request{Tier: "free", Endpoint: "/download"}

	da := m.Allow(free, "alice")
	db := m.Allow(free, "bob")
	if da.Remaining != 1 || db.Remaining != 1 {
		t.Errorf("Remaining alice=%d bob=%d, want 1 and 1 (independent counters)", da.Remaining, db.Remaining)
	}
}
