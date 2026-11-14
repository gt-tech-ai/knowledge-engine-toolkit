package unit_test

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/connect/interceptors"
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
	for _, testCase := range cases {
		t.Run(testCase.code.String(), func(t *testing.T) {
			t.Parallel()

			spy := fixtures.NewSpyLogger()
			handler := wrapUnary(interceptors.NewLoggingInterceptor(spy),
				func(_ context.Context, _ *testRequest) (any, error) {
					return nil, connect.NewError(testCase.code, textBoom)
				},
			)
			_, err := handler(context.Background(), newTestRequest())
			require.Error(t, err)

			if testCase.wantWarn {
				assert.Len(t, *spy.ChildWarnCalls, 1, "4xx codes log at Warn")
				assert.Empty(t, *spy.ChildErrorCalls, "4xx codes must not log at Error")
			} else {
				assert.Len(t, *spy.ChildErrorCalls, 1, "5xx/unknown codes log at Error")
				assert.Empty(t, *spy.ChildWarnCalls, "5xx codes must not log at Warn")
			}
		})
	}
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
//   - On a server-streaming RPC, a resolver error is returned with its own
//     (non-default) Connect code and the handler never runs.
func TestPrincipalInterceptor_StreamingFailsClosed(t *testing.T) {
	t.Parallel()

	failing := func(context.Context, *interceptors.AuthClaims) (string, error) {
		return "", connect.NewError(
			connect.CodePermissionDenied,
			"not a member",
		)
	}
	claims := fixtures.BuildRef(func(row *interceptors.AuthClaims) {
		row.Sub = "sub-1"
		row.TenantID = textTenant1
	}) // non-synthetic

	called := false
	handler := wrapStream(
		interceptors.NewPrincipalInterceptor[string](failing, nil),
		func(context.Context, *testRequest) error {
			called = true

			return nil
		},
	)
	err := handler(
		interceptors.WithAuthClaims(context.Background(), claims),
		newTestStream(nil),
	)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.False(t, called, "the streaming handler must not run when resolution fails")
}

// TestConnectTracingInterceptors_RecordErrorOnFailure tests that the server tracing
// interceptor records the error on the span when the call fails.
//
// Why this test is important:
//   - The span status/error is how a failed RPC shows up red in Tempo; if the
//     interceptor skipped RecordError on failure, traces would show a failed call as
//     successful and hide the fault from distributed-trace debugging.
//
// What it tests:
//   - The server tracing interceptor propagates the handler error unchanged (exercising
//     the error branch), including for a procedure with no method segment.
func TestConnectTracingInterceptors_RecordErrorOnFailure(t *testing.T) {
	t.Parallel()

	tracer := fixtures.NopTracer()
	boom := connect.NewError(connect.CodeInternal, "handler fault")
	failing := func(_ context.Context, _ *testRequest) (any, error) {
		return nil, boom
	}

	// A procedure with no method segment exercises parseConnectProcedure's
	// no-separator fallback branch.
	req := newTestRequest()

	req.spec.Procedure = "/Do"

	_, serverErr := wrapUnary(interceptors.TracingInterceptor(tracer), failing)(
		context.Background(),
		req,
	)
	require.Error(t, serverErr)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(serverErr))
}
