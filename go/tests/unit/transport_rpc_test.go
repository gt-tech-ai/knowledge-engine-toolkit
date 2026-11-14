package unit_test

import (
	"context"
	"slices"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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

	tests := testToConnectErrorAllCodes()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := rpc.ToConnectError(context.Background(), tt.appErr)
			assert.Equal(t, tt.wantCode, connect.CodeOf(result))
		})
	}
}

// testToConnectErrorAllCodesRow is one row of testToConnectErrorAllCodesTable.
type testToConnectErrorAllCodesRow struct {
	appErr   error
	name     string
	wantCode connect.Code
}

// testToConnectErrorAllCodes builds the table testToConnectErrorAllCodes
// runs.
func testToConnectErrorAllCodes() []testToConnectErrorAllCodesRow {
	return slices.Concat(
		testToConnectErrorClientCodes(),
		testToConnectErrorServerCodes(),
	)
}

// testToConnectErrorClientCodes lists part of the testToConnectErrorAllCodes rows.
func testToConnectErrorClientCodes() []testToConnectErrorAllCodesRow {
	return []testToConnectErrorAllCodesRow{
		{
			appErr:   apperr.NotFound(textNotFound),
			name:     textNotfound,
			wantCode: connect.CodeNotFound,
		},
		{
			appErr:   apperr.InvalidInput("bad input"),
			name:     textInvalidinput,
			wantCode: connect.CodeInvalidArgument,
		},
		{
			appErr:   apperr.Conflict("duplicate"),
			name:     textConflict,
			wantCode: connect.CodeAlreadyExists,
		},
		{
			appErr:   apperr.Unauthorized("no token"),
			name:     textUnauthorized,
			wantCode: connect.CodeUnauthenticated,
		},
		{
			appErr:   apperr.Forbidden("not allowed"),
			name:     textForbidden,
			wantCode: connect.CodePermissionDenied,
		},
		{
			appErr:   apperr.Upstream("dependency down"),
			name:     textUpstream,
			wantCode: connect.CodeUnavailable,
		},
		{
			appErr:   apperr.Unavailable("dependency flapping"),
			name:     "Unavailable",
			wantCode: connect.CodeUnavailable,
		},
		{
			appErr:   apperr.New(apperr.CodeCanceled, "request canceled"),
			name:     textCanceled,
			wantCode: connect.CodeCanceled,
		},
	}
}

// testToConnectErrorServerCodes lists part of the testToConnectErrorAllCodes rows.
func testToConnectErrorServerCodes() []testToConnectErrorAllCodesRow {
	return []testToConnectErrorAllCodesRow{
		{
			appErr:   apperr.New(apperr.CodeResourceExhausted, "budget spent"),
			name:     "ResourceExhausted",
			wantCode: connect.CodeResourceExhausted,
		},
		{
			appErr:   apperr.Timeout("deadline exceeded"),
			name:     textTimeoutTitle,
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
		{appErr: apperr.Internal("db connection failed"), name: textInternalTitle},
		{appErr: apperr.Conflict("team member already exists"), name: textConflict},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ctx := apperr.WithSink(context.Background())

			_ = rpc.ToConnectError(ctx, testCase.appErr)
			assert.Equal(t, testCase.appErr, apperr.Captured(ctx),
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
		{appErr: apperr.NotFound(sensitiveMsg), name: textNotfound},
		{appErr: apperr.InvalidInput(sensitiveMsg), name: textInvalidinput},
		{appErr: apperr.Conflict(sensitiveMsg), name: textConflict},
		{appErr: apperr.Unauthorized(sensitiveMsg), name: textUnauthorized},
		{appErr: apperr.Forbidden(sensitiveMsg), name: textForbidden},
		{appErr: apperr.Upstream(sensitiveMsg), name: textUpstream},
		{appErr: apperr.Internal(sensitiveMsg), name: textInternalTitle},
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
	return slices.Concat(fromRPCClientFaultCases(), fromRPCServerFaultCases())
}

// fromRPCClientFaultCases builds the table fromRPCCases runs.
func fromRPCClientFaultCases() []fromRPCCase {
	return []fromRPCCase{
		fromRPCRow(
			textNotfound,
			connect.CodeNotFound,
			codes.NotFound,
			apperr.CodeNotFound,
		),
		fromRPCRow(
			"InvalidArgument",
			connect.CodeInvalidArgument,
			codes.InvalidArgument,
			apperr.CodeInvalidInput,
		),
		fromRPCRow(
			"AlreadyExists",
			connect.CodeAlreadyExists,
			codes.AlreadyExists,
			apperr.CodeConflict,
		),
		fromRPCRow(
			"Unauthenticated",
			connect.CodeUnauthenticated,
			codes.Unauthenticated,
			apperr.CodeUnauthorized,
		),
		fromRPCRow(
			"PermissionDenied",
			connect.CodePermissionDenied,
			codes.PermissionDenied,
			apperr.CodeForbidden,
		),
	}
}

// fromRPCServerFaultCases holds the remaining fromRPCCase rows.
func fromRPCServerFaultCases() []fromRPCCase {
	return []fromRPCCase{
		fromRPCRow(
			"Unavailable",
			connect.CodeUnavailable,
			codes.Unavailable,
			apperr.CodeUpstream,
		),
		fromRPCRow(
			"DeadlineExceeded",
			connect.CodeDeadlineExceeded,
			codes.DeadlineExceeded,
			apperr.CodeTimeout,
		),
		fromRPCRow(
			textCanceled,
			connect.CodeCanceled,
			codes.Canceled,
			apperr.CodeCanceled,
		),
		fromRPCRow(
			textInternalTitle,
			connect.CodeInternal,
			codes.Internal,
			apperr.CodeInternal,
		),
		fromRPCRow(
			"Unimplemented",
			connect.CodeUnimplemented,
			codes.Unimplemented,
			apperr.CodeInvalidInput,
		),
		fromRPCRow("DataLoss", connect.CodeDataLoss, codes.DataLoss, apperr.CodeUnknown),
	}
}

// fromRPCRow is one wire-code row of the fromRPC table.
func fromRPCRow(
	name string,
	c connect.Code,
	g codes.Code,
	want apperr.ErrorCode,
) fromRPCCase {
	return fromRPCCase{name: name, connect: c, grpc: g, wantCode: want}
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

	for _, testCase := range fromRPCCases() {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			wire := connect.NewError(
				testCase.connect,
				"peer: "+testCase.name,
			)
			got := rpc.FromRPCError(wire)
			assert.True(
				t,
				apperr.Is(got, testCase.wantCode),
				"want %s, got %v",
				testCase.wantCode,
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

	for _, testCase := range fromRPCCases() {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			wire := status.Error(testCase.grpc, "peer: "+testCase.name)
			got := rpc.FromRPCError(wire)
			assert.True(
				t,
				apperr.Is(got, testCase.wantCode),
				"want %s, got %v",
				testCase.wantCode,
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
		{name: textNotfound, appErr: apperr.NotFound(textX)},
		{name: textInvalidinput, appErr: apperr.InvalidInput(textX)},
		{name: textConflict, appErr: apperr.Conflict(textX)},
		{name: textUnauthorized, appErr: apperr.Unauthorized(textX)},
		{name: textForbidden, appErr: apperr.Forbidden(textX)},
		{name: textUpstream, appErr: apperr.Upstream(textX)},
		{name: textCanceled, appErr: apperr.New(apperr.CodeCanceled, textX)},
		{name: textTimeoutTitle, appErr: apperr.Timeout(textX)},
		{name: textInternalTitle, appErr: apperr.Internal(textX)},
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

const (
	// textCanceled is the literal "Canceled".
	textCanceled = "Canceled"

	// textConflict is the literal "Conflict".
	textConflict = "Conflict"

	// textForbidden is the literal "Forbidden".
	textForbidden = "Forbidden"

	// textInternalTitle is the literal "Internal".
	textInternalTitle = "Internal"

	// textNotfound is the literal "NotFound".
	textNotfound = "NotFound"

	// textUnauthorized is the literal "Unauthorized".
	textUnauthorized = "Unauthorized"

	// textUpstream is the literal "Upstream".
	textUpstream = "Upstream"
)
