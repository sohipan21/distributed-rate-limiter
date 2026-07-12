// Package grpcapi exposes rate-limit decisions over grpc, wrapping the same
// policy manager as the http api — one counter, two transports
package grpcapi

import (
	"context"
	"math"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	ratelimitv1 "github.com/sohipan21/distributed-rate-limiter/gen/ratelimit/v1"
	"github.com/sohipan21/distributed-rate-limiter/internal/policy"
)

type Server struct {
	ratelimitv1.UnimplementedRateLimiterServer
	manager *policy.Manager
}

func NewServer(m *policy.Manager) *Server {
	return &Server{manager: m}
}

func (s *Server) Check(ctx context.Context, req *ratelimitv1.CheckRequest) (*ratelimitv1.CheckResponse, error) {
	if req.GetIdentity() == "" {
		return nil, status.Error(codes.InvalidArgument, "identity is required")
	}

	preq := policy.Request{Tier: req.GetTier(), Endpoint: req.GetEndpoint()}
	d := s.manager.Allow(preq, req.GetIdentity())

	// same round-up as the http handler (internal/httpapi/handler.go):
	// never tell a client to retry too early
	var retryAfter int64
	if !d.Allowed {
		retryAfter = int64(math.Ceil(d.RetryAfter.Seconds()))
		if retryAfter < 1 {
			retryAfter = 1
		}
	}

	return &ratelimitv1.CheckResponse{
		Allowed:           d.Allowed,
		Limit:             int64(s.manager.Resolve(preq).Config.Limit),
		Remaining:         int64(d.Remaining),
		RetryAfterSeconds: retryAfter,
		ResetAtUnix:       d.ResetAt.Unix(),
	}, nil
}
