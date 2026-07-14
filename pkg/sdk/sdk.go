// Package sdk is a drop-in rate-limit client and http middleware. Point it at
// a running rate-limiter service and wrap any http.Handler; the middleware
// checks each request and returns 429 with the standard headers when denied.
package sdk

import (
	"context"
	"time"
)

// what identifies a request to the limiter
type Request struct {
	Identity string
	Tier     string
	Endpoint string
}

// the limiter's answer, mirroring the service's response contract
type Decision struct {
	Allowed    bool
	Limit      int
	Remaining  int
	RetryAfter time.Duration
	ResetAt    time.Time
}

// Checker is what the middleware talks to. Client is the gRPC implementation;
// the interface keeps the middleware independent of transport
type Checker interface {
	Check(ctx context.Context, req Request) (Decision, error)
}
