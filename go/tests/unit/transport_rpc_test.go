package unit_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/rpc"
)

// ---------------------------------------------------------------------------
// ToConnectError — table-driven code mapping
// ---------------------------------------------------------------------------

// TestToConnectError_AllCodes tests that every application error type is
// mapped to the correct Connect RPC status code.
//
// Why this test is important:
//   - Incorrect code mapping causes clients to apply the wrong retry/backoff
//     strategy (e.g. retrying a 400 InvalidInput indefinitely)
//   - The mapping is the API contract boundary between Go services and their
//     clients; a wrong code is a breaking change
//
// What it tests:
//   - NotFound maps to CodeNotFound
//   - InvalidInput maps to CodeInvalidArgument
//   - Conflict maps to CodeAlreadyExists
//   - Unauthorized maps to CodeUnauthenticated
//   - Forbidden maps to CodePermissionDenied
//   - Upstream maps to CodeUnavailable
//   - Unavailable maps to CodeUnavailable (so a client can retry it)
//   - Canceled maps to CodeCanceled
//   - ResourceExhausted maps to CodeResourceExhausted
//   - Internal and unknown errors map to CodeInternal
func TestToConnectError_AllCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		appErr   error
		name     string
		wantCode connect.Code
	}{
		{
			appErr:   apperr.NotFound("not found"),
			name:     "NotFound",
			wantCode: connect.CodeNotFound,
		},
		{
			appErr:   apperr.InvalidInput("bad input"),
			name:     "InvalidInput",
			wantCode: connect.CodeInvalidArgument,
		},
		{
			appErr:   apperr.Conflict("duplicate"),
			name:     "Conflict",
			wantCode: connect.CodeAlreadyExists,
		},
		{
			appErr:   apperr.Unauthorized("no token"),
			name:     "Unauthorized",
			wantCode: connect.CodeUnauthenticated,
		},
		{
			appErr:   apperr.Forbidden("not allowed"),
			name:     "Forbidden",
			wantCode: connect.CodePermissionDenied,
		},
		{
			appErr:   apperr.Upstream("dependency down"),
			name:     "Upstream",
			wantCode: connect.CodeUnavailable,
		},
		{
			appErr:   apperr.Unavailable("dependency flapping"),
			name:     "Unavailable",
			wantCode: connect.CodeUnavailable,
		},
		{
			appErr:   apperr.New(apperr.CodeCanceled, "request canceled"),
			name:     "Canceled",
			wantCode: connect.CodeCanceled,
		},
		{
			appErr:   apperr.New(apperr.CodeResourceExhausted, "budget spent"),
			name:     "ResourceExhausted",
			wantCode: connect.CodeResourceExhausted,
		},
		{
			appErr:   apperr.Timeout("deadline exceeded"),
			name:     "Timeout",
			wantCode: connect.CodeDeadlineExceeded,
		},
		{
			appErr:   apperr.Internal("something broke"),
			name:     "Internal (default)",
			wantCode: connect.CodeInternal,
		},
		{
			appErr:   apperr.Sentinel("random error"),
			name:     "Unknown code (default)",
			wantCode: connect.CodeInternal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := rpc.ToConnectError(context.Background(), tt.appErr)
			assert.Equal(t, tt.wantCode, connect.CodeOf(result))
		})
	}
}

// ---------------------------------------------------------------------------
// ToConnectError — captures the real error for the request logger
// ---------------------------------------------------------------------------

// TestToConnectError_CapturesRealError tests that the real error is captured
// into the request error sink so the single request-logging interceptor can
// record the true cause, while the client receives only a sanitized error.
//
// Why this test is important:
//   - The request logger relies on the sink to record the real failure; if
//     ToConnectError stopped capturing, failures would be logged with only the
//     sanitized message and the true cause would be lost
//   - Capture must happen for client (4xx) errors too, not just 5xx — that is
//     the whole point of surfacing the actual error (e.g. a Conflict)
//
// What it tests:
//   - After mapping, apperr.Captured returns the original error for both a
//     server (Internal) and a client (Conflict) error
func TestToConnectError_CapturesRealError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		appErr error
		name   string
	}{
		{appErr: apperr.Internal("db connection failed"), name: "Internal"},
		{appErr: apperr.Conflict("team member already exists"), name: "Conflict"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := apperr.WithSink(context.Background())
			_ = rpc.ToConnectError(ctx, tt.appErr)
			assert.Equal(t, tt.appErr, apperr.Captured(ctx),
				"the real error must be captured for the request logger")
		})
	}
}

// ---------------------------------------------------------------------------
// ToConnectError — client-safe messages
// ---------------------------------------------------------------------------

// TestToConnectError_ClientSafeMessages tests that the original internal error
// message is never surfaced in the Connect error returned to clients.
//
// Why this test is important:
//   - Internal messages often contain SQL queries, stack traces, or user PII;
//     leaking them to the client is a security vulnerability under OWASP A05
//   - Every error code branch must sanitize its message independently
//
// What it tests:
//   - For all error types, Connect error text does not contain the original
//     sensitive message
func TestToConnectError_ClientSafeMessages(t *testing.T) {
	t.Parallel()

	sensitiveMsg := "SQL error: relation users does not exist"
	tests := []struct {
		appErr error
		name   string
	}{
		{appErr: apperr.NotFound(sensitiveMsg), name: "NotFound"},
		{appErr: apperr.InvalidInput(sensitiveMsg), name: "InvalidInput"},
		{appErr: apperr.Conflict(sensitiveMsg), name: "Conflict"},
		{appErr: apperr.Unauthorized(sensitiveMsg), name: "Unauthorized"},
		{appErr: apperr.Forbidden(sensitiveMsg), name: "Forbidden"},
		{appErr: apperr.Upstream(sensitiveMsg), name: "Upstream"},
		{appErr: apperr.Internal(sensitiveMsg), name: "Internal"},
		{appErr: apperr.Sentinel(sensitiveMsg), name: "Unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := rpc.ToConnectError(context.Background(), tt.appErr)
			assert.NotContains(t, result.Error(), sensitiveMsg,
				"Connect error message should not contain original error text")
		})
	}
}

// ---------------------------------------------------------------------------
// FromRPCError — Connect and gRPC → AppError
// ---------------------------------------------------------------------------

// fromRPCCase is one row of the Connect/gRPC FromRPCError code table.
type fromRPCCase struct {
	// name labels the subtest.
	name string
	// wantCode is the domain code FromRPCError must return.
	wantCode apperr.ErrorCode
	// connect is the Connect wire code under test.
	connect connect.Code
	// grpc is the matching gRPC status code under test.
	grpc codes.Code
}

// fromRPCCases returns the shared code table for Connect and gRPC FromRPCError tests.
func fromRPCCases() []fromRPCCase {
	return []fromRPCCase{
		{
			name:     "NotFound",
			connect:  connect.CodeNotFound,
			grpc:     codes.NotFound,
			wantCode: apperr.CodeNotFound,
		},
		{
			name:     "InvalidArgument",
			connect:  connect.CodeInvalidArgument,
			grpc:     codes.InvalidArgument,
			wantCode: apperr.CodeInvalidInput,
		},
		{
			name:     "AlreadyExists",
			connect:  connect.CodeAlreadyExists,
			grpc:     codes.AlreadyExists,
			wantCode: apperr.CodeConflict,
		},
		{
			name:     "Unauthenticated",
			connect:  connect.CodeUnauthenticated,
			grpc:     codes.Unauthenticated,
			wantCode: apperr.CodeUnauthorized,
		},
		{
			name:     "PermissionDenied",
			connect:  connect.CodePermissionDenied,
			grpc:     codes.PermissionDenied,
			wantCode: apperr.CodeForbidden,
		},
		{
			name:     "Unavailable",
			connect:  connect.CodeUnavailable,
			grpc:     codes.Unavailable,
			wantCode: apperr.CodeUpstream,
		},
		{
			name:     "DeadlineExceeded",
			connect:  connect.CodeDeadlineExceeded,
			grpc:     codes.DeadlineExceeded,
			wantCode: apperr.CodeTimeout,
		},
		{
			name:     "Canceled",
			connect:  connect.CodeCanceled,
			grpc:     codes.Canceled,
			wantCode: apperr.CodeCanceled,
		},
		{
			name:     "Internal",
			connect:  connect.CodeInternal,
			grpc:     codes.Internal,
			wantCode: apperr.CodeInternal,
		},
		{
			name:     "Unimplemented",
			connect:  connect.CodeUnimplemented,
			grpc:     codes.Unimplemented,
			wantCode: apperr.CodeInvalidInput,
		},
		{
			name:     "DataLoss",
			connect:  connect.CodeDataLoss,
			grpc:     codes.DataLoss,
			wantCode: apperr.CodeUnknown,
		},
	}
}

// TestFromRPCError_ConnectCodes tests that Connect wire codes map to the
// expected domain AppError codes.
//
// Why this test is important:
//   - Clients calling Connect peers must classify failures for retry and
//     ToConnectError re-emission; a wrong mapping breaks that contract
//
// What it tests:
//   - Each mapped Connect code yields the inverse of ToConnectError
//   - Unimplemented is the caller's error (CodeInvalidInput, never retried); unmapped
//     codes fall through to CodeUnknown
func TestFromRPCError_ConnectCodes(t *testing.T) {
	t.Parallel()

	for _, tt := range fromRPCCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			wire := connect.NewError(tt.connect, apperr.Sentinel("peer: "+tt.name))
			got := rpc.FromRPCError(wire)
			assert.True(
				t,
				apperr.Is(got, tt.wantCode),
				"want %s, got %v",
				tt.wantCode,
				got,
			)
			assert.ErrorIs(t, got, wire, "original wire error must remain in the chain")
		})
	}
}

// TestFromRPCError_GRPCStatusCodes tests that raw gRPC status errors map to
// domain AppError codes (Connect's CodeOf does not unwrap status.Error).
//
// Why this test is important:
//   - Internal gRPC clients return status.Error, not
//     *connect.Error; without the gRPC fallback path those failures would
//     always collapse to Internal
//
// What it tests:
//   - status.Error for each mapped code yields the same AppError code as Connect
//   - Unmapped codes fall through to CodeUnknown
func TestFromRPCError_GRPCStatusCodes(t *testing.T) {
	t.Parallel()

	for _, tt := range fromRPCCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			wire := status.Error(tt.grpc, "peer: "+tt.name)
			got := rpc.FromRPCError(wire)
			assert.True(
				t,
				apperr.Is(got, tt.wantCode),
				"want %s, got %v",
				tt.wantCode,
				got,
			)
			assert.ErrorIs(t, got, wire, "original wire error must remain in the chain")
		})
	}
}

// TestFromRPCError_Passthrough tests nil, already-AppError, and plain-error
// edge cases for FromRPCError.
//
// Why this test is important:
//   - Callers may pass nil or an AppError that was already classified; wrapping
//     again would lose the original domain code or invent a spurious Internal
//
// What it tests:
//   - nil → nil
//   - existing AppError returned unchanged
//   - a plain uncoded error → CodeUnknown
func TestFromRPCError_Passthrough(t *testing.T) {
	t.Parallel()

	require.NoError(t, rpc.FromRPCError(nil))

	existing := apperr.NotFound("already domain")
	assert.Same(t, existing, rpc.FromRPCError(existing))

	plain := apperr.Sentinel("not an rpc error")
	got := rpc.FromRPCError(plain)
	assert.True(t, apperr.Is(got, apperr.CodeUnknown))
	assert.ErrorIs(t, got, plain)
}

// TestFromRPCError_RoundTrip tests that FromRPCError recovers the domain code
// that ToConnectError emitted.
//
// Why this test is important:
//   - Service-to-service hops sanitize via ToConnectError then reclassify via
//     FromRPCError; a broken round trip silently changes failure semantics
//
// What it tests:
//   - For every code ToConnectError emits, FromRPCError restores that AppError code
func TestFromRPCError_RoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		appErr error
		name   string
	}{
		{name: "NotFound", appErr: apperr.NotFound("x")},
		{name: "InvalidInput", appErr: apperr.InvalidInput("x")},
		{name: "Conflict", appErr: apperr.Conflict("x")},
		{name: "Unauthorized", appErr: apperr.Unauthorized("x")},
		{name: "Forbidden", appErr: apperr.Forbidden("x")},
		{name: "Upstream", appErr: apperr.Upstream("x")},
		{name: "Canceled", appErr: apperr.New(apperr.CodeCanceled, "x")},
		{name: "Timeout", appErr: apperr.Timeout("x")},
		{name: "Internal", appErr: apperr.Internal("x")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			want := apperr.Code(tt.appErr)
			wire := rpc.ToConnectError(context.Background(), tt.appErr)
			got := rpc.FromRPCError(wire)
			assert.Equal(t, want, apperr.Code(got))
		})
	}
}
