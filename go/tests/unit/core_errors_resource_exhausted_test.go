package unit_test

import (
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/retry/exponential"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/rpc"
)

// TestResourceExhausted_MapsGRPC8AndHTTP429 tests that the quota code maps to
// gRPC RESOURCE_EXHAUSTED and HTTP 429 at every transport edge.
//
// Why this test is important:
//   - A caller decides whether to back off (429 / RESOURCE_EXHAUSTED) or fail
//     from the wire status; a quota rejection surfacing as 500 would read as a
//     server bug and trigger alerts instead of client back-off
//
// What it tests:
//   - CodeResourceExhausted has the wire value "RESOURCE_EXHAUSTED"
//   - ToHTTPStatus returns 429
//   - rpc.Sanitize returns connect.CodeResourceExhausted (gRPC 8) with a fixed message
func TestResourceExhausted_MapsGRPC8AndHTTP429(t *testing.T) {
	t.Parallel()

	err := apperr.New(apperr.CodeResourceExhausted, "org token budget spent")

	assert.Equal(t, apperr.CodeResourceExhausted, apperr.ErrorCode("RESOURCE_EXHAUSTED"))
	assert.Equal(
		t,
		http.StatusTooManyRequests,
		apperr.ToHTTPStatus(apperr.CodeResourceExhausted),
	)
	code, msg := rpc.Sanitize(err)
	assert.Equal(t, connect.CodeResourceExhausted, code)
	assert.Equal(t, connect.Code(8), code)
	assert.Equal(t, "resource exhausted", msg)
}

// TestResourceExhausted_IsNeitherTransientNorPermanent tests that a quota
// rejection is classified neither transient nor permanent.
//
// Why this test is important:
//   - A retry loop that treats a spent budget as transient hammers the quota
//     check until the window resets; a permanent classification dead-letters work
//     that would succeed once the budget window rolls over
//
// What it tests:
//   - IsTransient returns false for CodeResourceExhausted
//   - IsPermanent returns false for CodeResourceExhausted
func TestResourceExhausted_IsNeitherTransientNorPermanent(t *testing.T) {
	t.Parallel()

	err := apperr.New(apperr.CodeResourceExhausted, "org token budget spent")

	assert.False(t, apperr.IsTransient(err))
	assert.False(t, apperr.IsPermanent(err))
}

// TestClassify_GRPCResourceExhaustedIsQuotaNotTransient tests that a wire
// RESOURCE_EXHAUSTED maps back to the quota code and is not retried.
//
// Why this test is important:
//   - A Go client receiving a quota rejection from a peer must not treat it as a
//     transient outage; the exponential retrier and circuit breaker share
//     IsRetryable, so a retried quota error also trips the breaker on a healthy peer
//
// What it tests:
//   - rpc.FromRPCError on a gRPC status ResourceExhausted returns CodeResourceExhausted
//   - IsTransient is false and exponential.IsRetryable is false for it
func TestClassify_GRPCResourceExhaustedIsQuotaNotTransient(t *testing.T) {
	t.Parallel()

	err := rpc.FromRPCError(status.Error(codes.ResourceExhausted, "budget spent"))

	assert.Equal(t, apperr.CodeResourceExhausted, apperr.Code(err))
	assert.False(t, apperr.IsTransient(err))
	assert.False(t, exponential.IsRetryable(err))
}
