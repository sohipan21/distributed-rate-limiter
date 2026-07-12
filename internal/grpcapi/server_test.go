package grpcapi

import (
	"context"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	ratelimitv1 "github.com/sohipan21/distributed-rate-limiter/gen/ratelimit/v1"
	"github.com/sohipan21/distributed-rate-limiter/internal/httpapi"
	"github.com/sohipan21/distributed-rate-limiter/internal/limiter"
	"github.com/sohipan21/distributed-rate-limiter/internal/policy"
)

func testPolicies(t *testing.T) *policy.Policies {
	t.Helper()
	tb := func(n int) policy.Limit {
		return policy.Limit{
			Algorithm: limiter.TokenBucketAlgorithm,
			Config:    limiter.Config{Limit: n, Window: time.Minute},
		}
	}
	p, err := policy.NewPolicies(
		tb(10),
		policy.Rule{Tier: "free", Limit: tb(2)},
		policy.Rule{Tier: "paid", Limit: tb(5)},
	)
	if err != nil {
		t.Fatalf("NewPolicies: %v", err)
	}
	return p
}

// in-process grpc client over bufconn: no ports, no redis
func testClient(t *testing.T, m *policy.Manager) ratelimitv1.RateLimiterClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	ratelimitv1.RegisterRateLimiterServer(srv, NewServer(m))
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return ratelimitv1.NewRateLimiterClient(conn)
}

func TestCheckAllowed(t *testing.T) {
	c := testClient(t, policy.NewManager(testPolicies(t)))

	resp, err := c.Check(context.Background(), &ratelimitv1.CheckRequest{
		Identity: "alice", Tier: "free", Endpoint: "/download",
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !resp.GetAllowed() {
		t.Error("allowed = false, want true")
	}
	if resp.GetLimit() != 2 {
		t.Errorf("limit = %d, want 2", resp.GetLimit())
	}
	if resp.GetRemaining() != 1 {
		t.Errorf("remaining = %d, want 1", resp.GetRemaining())
	}
	if resp.GetRetryAfterSeconds() != 0 {
		t.Errorf("retry_after_seconds = %d, want 0 on allow", resp.GetRetryAfterSeconds())
	}
}

func TestCheckDenied(t *testing.T) {
	c := testClient(t, policy.NewManager(testPolicies(t)))
	req := &ratelimitv1.CheckRequest{Identity: "alice", Tier: "free", Endpoint: "/download"}

	ctx := context.Background()
	c.Check(ctx, req)
	c.Check(ctx, req)
	resp, err := c.Check(ctx, req)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if resp.GetAllowed() {
		t.Fatal("allowed = true, want false")
	}
	if resp.GetRemaining() != 0 {
		t.Errorf("remaining = %d, want 0", resp.GetRemaining())
	}
	if resp.GetRetryAfterSeconds() < 1 {
		t.Errorf("retry_after_seconds = %d, want >= 1", resp.GetRetryAfterSeconds())
	}
	if resp.GetResetAtUnix() <= time.Now().Unix()-1 {
		t.Errorf("reset_at_unix = %d, want in the future", resp.GetResetAtUnix())
	}
}

func TestCheckPolicyRouting(t *testing.T) {
	c := testClient(t, policy.NewManager(testPolicies(t)))

	resp, err := c.Check(context.Background(), &ratelimitv1.CheckRequest{
		Identity: "bob", Tier: "paid", Endpoint: "/download",
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if resp.GetLimit() != 5 {
		t.Errorf("paid limit = %d, want 5", resp.GetLimit())
	}
}

func TestCheckMissingIdentity(t *testing.T) {
	c := testClient(t, policy.NewManager(testPolicies(t)))

	_, err := c.Check(context.Background(), &ratelimitv1.CheckRequest{Tier: "free"})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("error code = %v, want InvalidArgument", status.Code(err))
	}
}

// one manager, two transports, one counter
func TestCrossTransportParity(t *testing.T) {
	m := policy.NewManager(testPolicies(t))
	c := testClient(t, m)
	h := httpapi.Handler(m)

	ctx := context.Background()
	req := &ratelimitv1.CheckRequest{Identity: "alice", Tier: "paid", Endpoint: "/x"}

	r1, _ := c.Check(ctx, req) // remaining 4
	r2, _ := c.Check(ctx, req) // remaining 3
	if r1.GetRemaining() != 4 || r2.GetRemaining() != 3 {
		t.Fatalf("grpc remaining = %d, %d; want 4, 3", r1.GetRemaining(), r2.GetRemaining())
	}

	// the http api sees the same counter
	hr := httptest.NewRequest("POST", "/check",
		strings.NewReader(`{"identity":"alice","tier":"paid","endpoint":"/x"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, hr)
	if got := w.Header()["X-RateLimit-Remaining"]; len(got) == 0 || got[0] != "2" {
		t.Errorf("http X-RateLimit-Remaining = %v, want [2] (shared counter)", got)
	}
}
