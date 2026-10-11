package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	grpcinterceptors "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc/grpc/interceptors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// TestGRPCInterceptors_ErrorBranches tests that the metrics and tracing unary
// interceptors label/record a failed call and pass its status through unchanged.
//
// Why this test is important:
//   - The error-code label on grpc_*_requests_total and the error status on the span
//     are what make a failing RPC visible in dashboards and traces; skipping them on
//     the error path hides real faults. The interceptors must also be transparent —
//     the handler's status code passes through untouched.
//
// What it tests:
//   - Server/client metrics + server/client tracing interceptors each return the
//     handler's Internal error unchanged (exercising their err != nil branches).
//   - parseGRPCMethod falls back to ("", fullMethod) for a malformed method string.
func TestGRPCInterceptors_ErrorBranches(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	boom := status.Error(codes.Internal, "handler fault")

	var failHandler grpc.UnaryHandler = func(context.Context, any) (any, error) {
		return nil, boom
	}
	failInvoker := func(
		context.Context,
		string,
		any,
		any,
		*grpc.ClientConn,
		...grpc.CallOption,
	) error {
		return boom
	}

	t.Run("metrics server labels the error code", func(t *testing.T) {
		t.Parallel()
		ic := grpcinterceptors.MetricsServerInterceptor(fixtures.NopMetrics())
		_, err := ic(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc.S/M"}, failHandler)
		require.Error(t, err)
		assert.Equal(t, codes.Internal, status.Code(err))
	})

	t.Run("metrics client labels the error code", func(t *testing.T) {
		t.Parallel()
		ic := grpcinterceptors.MetricsClientInterceptor(fixtures.NopMetrics())
		err := ic(ctx, "/svc.S/M", nil, nil, nil, failInvoker)
		require.Error(t, err)
		assert.Equal(t, codes.Internal, status.Code(err))
	})

	t.Run("metrics server tolerates a malformed method", func(t *testing.T) {
		t.Parallel()
		ic := grpcinterceptors.MetricsServerInterceptor(fixtures.NopMetrics())
		// A method with no service/method separator exercises parseGRPCMethod's fallback.
		_, err := ic(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "malformed"},
			func(context.Context, any) (any, error) { return "ok", nil })
		require.NoError(t, err)
	})

	t.Run("tracing server records the error on the span", func(t *testing.T) {
		t.Parallel()
		ic := grpcinterceptors.TracingServerInterceptor(fixtures.NopTracer())
		_, err := ic(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc.S/M"}, failHandler)
		require.Error(t, err)
		assert.Equal(t, codes.Internal, status.Code(err))
	})

	t.Run("tracing client records the error on the span", func(t *testing.T) {
		t.Parallel()
		ic := grpcinterceptors.TracingClientInterceptor(fixtures.NopTracer())
		err := ic(ctx, "/svc.S/M", nil, nil, nil, failInvoker)
		require.Error(t, err)
		assert.Equal(t, codes.Internal, status.Code(err))
	})
}

// TestGRPCRetryClientInterceptor_ExhaustedBecomesUnavailable tests that a retryable
// call that never recovers surfaces as Unavailable once the retry budget is spent.
//
// Why this test is important:
//   - Callers switch on the status code; a retries-exhausted transient failure must
//     present as Unavailable (retry-later) rather than leaking the last raw error, so
//     upstream retry/backoff logic stays correct.
//
// What it tests:
//   - An invoker that always fails with a retryable code, driven through a retrier
//     that gives up, returns codes.Unavailable naming retry exhaustion.
func TestGRPCRetryClientInterceptor_ExhaustedBecomesUnavailable(t *testing.T) {
	t.Parallel()

	retrier, attempts := fixtures.StubRetrier(
		0,
		true,
	) // alwaysFail → retries then gives up
	ic := grpcinterceptors.RetryClientInterceptor(retrier)

	err := ic(
		context.Background(),
		"/svc.S/M",
		nil,
		nil,
		nil,
		func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			return status.Error(codes.Unavailable, "transient")
		},
	)

	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err))
	assert.Contains(t, err.Error(), "retries exhausted")
	assert.Greater(
		t,
		attempts(),
		1,
		"a retryable failure is retried before it is declared exhausted",
	)
}
