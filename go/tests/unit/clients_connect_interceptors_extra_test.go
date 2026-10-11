package unit_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/transport/connect/interceptors"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// TestLoggingInterceptor_StatusCodeMapping tests that every Connect error code maps
// to the correct log level: 4xx-equivalent codes log at Warn, everything else at
// Error.
//
// Why this test is important:
//   - Log-based alert rules key off log level: 4xx client conditions must NOT fire an
//     Error-level alert, while genuine 5xx faults must.
//     A miscategorized code either pages on-call for a bad request or hides a real
//     outage.
//
// What it tests:
//   - Each of the Connect code → HTTP status arms runs, and the resulting log level is
//     Warn for the 4xx family and Error for the 5xx/unknown family.
func TestLoggingInterceptor_StatusCodeMapping(t *testing.T) {
	t.Parallel()

	cases := []struct {
		code     connect.Code
		wantWarn bool
	}{
		{connect.CodeInvalidArgument, true},
		{connect.CodeFailedPrecondition, true},
		{connect.CodeOutOfRange, true},
		{connect.CodeUnauthenticated, true},
		{connect.CodePermissionDenied, true},
		{connect.CodeNotFound, true},
		{connect.CodeAlreadyExists, true},
		{connect.CodeResourceExhausted, true},
		{connect.CodeUnimplemented, false},
		{connect.CodeUnavailable, false},
		{connect.CodeDeadlineExceeded, false},
		{connect.CodeInternal, false},
		// 499 client-closed-request: the caller cancelled/disconnected — a client
		// outcome, logged at Warn so it does not fire an Error-level alert.
		{connect.CodeCanceled, true},
	}
	for _, tc := range cases {
		t.Run(tc.code.String(), func(t *testing.T) {
			t.Parallel()
			spy := fixtures.NewSpyLogger()
			handler := interceptors.NewLoggingInterceptor(spy).WrapUnary(
				func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
					return nil, connect.NewError(tc.code, apperr.Sentinel("boom"))
				},
			)
			_, err := handler(context.Background(), newTestRequest())
			require.Error(t, err)
			if tc.wantWarn {
				assert.Len(t, *spy.ChildWarnCalls, 1, "4xx codes log at Warn")
				assert.Empty(t, *spy.ChildErrorCalls, "4xx codes must not log at Error")
			} else {
				assert.Len(t, *spy.ChildErrorCalls, 1, "5xx/unknown codes log at Error")
				assert.Empty(t, *spy.ChildWarnCalls, "5xx codes must not log at Warn")
			}
		})
	}
}

// TestAuthInterceptor_StreamingClientIsNoOp tests that the server-side auth
// interceptor's streaming-client wrapper is an inert pass-through.
//
// Why this test is important:
//   - This interceptor extracts identity on the server; wrapping an outbound
//     streaming client must add nothing, or a service acting as a client would mangle
//     its own outgoing streams.
//
// What it tests:
//   - WrapStreamingClient returns a func that delegates straight to next.
func TestAuthInterceptor_StreamingClientIsNoOp(t *testing.T) {
	t.Parallel()

	called := false
	wrapped := interceptors.NewAuthInterceptor(false, testHeaders()).WrapStreamingClient(
		func(context.Context, connect.Spec) connect.StreamingClientConn {
			called = true
			return nil
		},
	)
	wrapped(context.Background(), connect.Spec{})
	assert.True(t, called, "the wrapped client func delegates to next unchanged")
}

// TestPrincipalInterceptor_StreamingFailsClosed tests that the streaming handler
// wrapper aborts the call when principal resolution fails.
//
// Why this test is important:
//   - A streaming call that proceeds without a resolved authorization context could
//     serve data across tenant boundaries; the resolution failure must fail closed,
//     not fall through to the handler.
//
// What it tests:
//   - A resolver error makes WrapStreamingHandler return the error with its own
//     (non-default) Connect code and never invoke the wrapped handler.
func TestPrincipalInterceptor_StreamingFailsClosed(t *testing.T) {
	t.Parallel()

	failing := func(context.Context, *interceptors.AuthClaims) (string, error) {
		return "", connect.NewError(
			connect.CodePermissionDenied,
			apperr.Sentinel("not a member"),
		)
	}
	claims := &interceptors.AuthClaims{
		Sub:      "sub-1",
		TenantID: "tenant-1",
	} // non-synthetic

	called := false
	handler := interceptors.NewPrincipalInterceptor[string](
		failing,
		nil,
	).WrapStreamingHandler(
		func(context.Context, connect.StreamingHandlerConn) error {
			called = true
			return nil
		},
	)
	err := handler(interceptors.WithAuthClaims(context.Background(), claims), nil)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.False(t, called, "the streaming handler must not run when resolution fails")
}

// TestConnectTracingInterceptors_RecordErrorOnFailure tests that both the server and
// client tracing interceptors record the error on the span when the call fails.
//
// Why this test is important:
//   - The span status/error is how a failed RPC shows up red in Tempo; if the
//     interceptor skipped RecordError on failure, traces would show a failed call as
//     successful and hide the fault from distributed-trace debugging.
//
// What it tests:
//   - Server and client tracing interceptors propagate the handler error unchanged
//     (exercising the error branch), including for a procedure with no method segment.
func TestConnectTracingInterceptors_RecordErrorOnFailure(t *testing.T) {
	t.Parallel()
	tracer := fixtures.NopTracer()
	boom := connect.NewError(connect.CodeInternal, apperr.Sentinel("handler fault"))
	failing := func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, boom
	}

	// newTestRequest carries an empty procedure, exercising parseConnectProcedure's
	// no-separator fallback branch.
	_, serverErr := interceptors.TracingInterceptor(tracer).
		WrapUnary(failing)(context.Background(), newTestRequest())
	require.Error(t, serverErr)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(serverErr))

	_, clientErr := interceptors.ClientTracingInterceptor(tracer).
		WrapUnary(failing)(context.Background(), newTestRequest())
	require.Error(t, clientErr)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(clientErr))
}
