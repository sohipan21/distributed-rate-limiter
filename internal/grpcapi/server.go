// Package grpcapi exposes rate-limit decisions over grpc, wrapping the same
// policy manager as the http api — one counter, two transports
package grpcapi

import (
	"context"
	"math"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	ratelimitv1 "github.com/sohipan21/distributed-rate-limiter/gen/ratelimit/v1"
	"github.com/sohipan21/distributed-rate-limiter/internal/auth"
	"github.com/sohipan21/distributed-rate-limiter/internal/policy"
)

type Server struct {
	ratelimitv1.UnimplementedRateLimiterServer
	manager *policy.Manager
	auth    auth.Authenticator
}

type Option func(*Server)

// WithAuth turns on api-key auth: the key travels in x-api-key metadata,
// and identity/tier come from the lookup instead of the request
func WithAuth(a auth.Authenticator) Option {
	return func(s *Server) { s.auth = a }
}

func NewServer(m *policy.Manager, opts ...Option) *Server {
	s := &Server{manager: m}
	for _, fn := range opts {
		fn(s)
	}
	return s
}

func (s *Server) Check(ctx context.Context, req *ratelimitv1.CheckRequest) (*ratelimitv1.CheckResponse, error) {
	identity, tier := req.GetIdentity(), req.GetTier()
	if s.auth != nil {
		var key string
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if v := md.Get("x-api-key"); len(v) > 0 {
				key = v[0]
			}
		}
		id, ok := s.auth.Lookup(ctx, key)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "invalid or missing api key")
		}
		identity, tier = id.Account, id.Tier
	} else if identity == "" {
		return nil, status.Error(codes.InvalidArgument, "identity is required")
	}

	preq := policy.Request{Tier: tier, Endpoint: req.GetEndpoint()}
	d := s.manager.Allow(preq, identity)

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
