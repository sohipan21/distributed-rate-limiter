package sdk

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	ratelimitv1 "github.com/sohipan21/distributed-rate-limiter/gen/ratelimit/v1"
)

// a canned server so this test proves the client's mapping without importing
// the real service
type fakeServer struct {
	ratelimitv1.UnimplementedRateLimiterServer
	resp *ratelimitv1.CheckResponse
	req  *ratelimitv1.CheckRequest
}

func (f *fakeServer) Check(ctx context.Context, req *ratelimitv1.CheckRequest) (*ratelimitv1.CheckResponse, error) {
	f.req = req
	return f.resp, nil
}

func TestClientMapsResponse(t *testing.T) {
	fake := &fakeServer{resp: &ratelimitv1.CheckResponse{
		Allowed:           false,
		Limit:             10,
		Remaining:         0,
		RetryAfterSeconds: 6,
		ResetAtUnix:       1783732312,
	}}

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	ratelimitv1.RegisterRateLimiterServer(srv, fake)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	d, err := NewClient(conn).Check(context.Background(), Request{Identity: "alice", Tier: "free", Endpoint: "/download"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	if d.Allowed || d.Limit != 10 || d.Remaining != 0 {
		t.Errorf("decision = %+v, want allowed=false limit=10 remaining=0", d)
	}
	if d.RetryAfter != 6*time.Second {
		t.Errorf("RetryAfter = %v, want 6s", d.RetryAfter)
	}
	if !d.ResetAt.Equal(time.Unix(1783732312, 0)) {
		t.Errorf("ResetAt = %v, want unix 1783732312", d.ResetAt)
	}
	// request fields propagate
	if fake.req.GetIdentity() != "alice" || fake.req.GetTier() != "free" || fake.req.GetEndpoint() != "/download" {
		t.Errorf("server saw %+v, want alice/free/download", fake.req)
	}
}
