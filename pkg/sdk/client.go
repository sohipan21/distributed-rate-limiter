package sdk

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	ratelimitv1 "github.com/sohipan21/distributed-rate-limiter/gen/ratelimit/v1"
)

// Client checks requests against the rate-limiter service over gRPC.
type Client struct {
	grpc ratelimitv1.RateLimiterClient
}

// NewClient wraps an existing connection; the caller owns dialing and creds.
func NewClient(conn *grpc.ClientConn) *Client {
	return &Client{grpc: ratelimitv1.NewRateLimiterClient(conn)}
}

// Dial is a convenience for the common case. It connects over plaintext, so
// use NewClient with your own credentials for anything but local/demo use.
func Dial(target string) (*Client, error) {
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return NewClient(conn), nil
}

func (c *Client) Check(ctx context.Context, req Request) (Decision, error) {
	resp, err := c.grpc.Check(ctx, &ratelimitv1.CheckRequest{
		Identity: req.Identity,
		Tier:     req.Tier,
		Endpoint: req.Endpoint,
	})
	if err != nil {
		return Decision{}, err
	}
	return Decision{
		Allowed:    resp.GetAllowed(),
		Limit:      int(resp.GetLimit()),
		Remaining:  int(resp.GetRemaining()),
		RetryAfter: time.Duration(resp.GetRetryAfterSeconds()) * time.Second,
		ResetAt:    time.Unix(resp.GetResetAtUnix(), 0),
	}, nil
}
