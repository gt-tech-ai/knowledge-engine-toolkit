package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	grpcinterceptors "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc/grpc/interceptors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/retry"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// errCaptured stops a captured call after its outgoing metadata is recorded; Aborted
// carries no domain code, so the client tier treats it as transient.
var errCaptured = status.Error(codes.Aborted, "captured")

// fastRetrier is a real exponential retrier making up to three attempts with
// millisecond backoff.
func fastRetrier(t *testing.T) decorate.Deps {
	t.Helper()
	r, err := retry.NewFromConfig(retry.Config{
		Kind:            retry.KindExponential,
		MaxRetries:      2,
		InitialInterval: time.Millisecond,
		MaxInterval:     2 * time.Millisecond,
		Multiplier:      2,
		MaxElapsedTime:  time.Second,
	})
	require.NoError(t, err)
	return decorate.Deps{Retrier: r}
}

// dialDecorated returns a lazily-connecting conn whose calls run through base and then
// reach the capturing interceptors, which stand in for the wire.
func dialDecorated(
	t *testing.T,
	base client.Base,
	token string,
	unary grpc.UnaryClientInterceptor,
	stream grpc.StreamClientInterceptor,
) *grpc.ClientConn {
	t.Helper()
	opts := append(grpcinterceptors.ClientOptions(base, token),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(unary),
		grpc.WithChainStreamInterceptor(stream),
	)
	conn, err := grpc.NewClient("passthrough:///decorated-client-test", opts...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// TestGRPCClientOptions_StampEveryAttempt tests the outgoing metadata of a decorated
// gRPC client.
//
// Why this test is important:
//   - An internal server that enforces service auth rejects any call without the bearer
//     token, so every unary attempt and every stream open must carry it — once, since
//     `authorization` is not a list header — and an empty token must attach nothing so a
//     local-dev server bypass keeps working.
//   - Without the traceparent on the wire the callee starts a disconnected trace and one
//     request fragments into a trace per service.
//
// What it tests:
//   - With retry on, each of the three attempts of a failing unary call carries exactly
//     one "Bearer tok-svc" and a traceparent naming the active trace; a stream open
//     carries both too.
//   - With an empty token neither call carries an authorization value.
func TestGRPCClientOptions_StampEveryAttempt(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	sc := sampledSpanContext()
	ctx := oteltrace.ContextWithSpanContext(context.Background(), sc)

	for _, tc := range []struct {
		name, token string
		want        []string
	}{
		{name: "configured token", token: "tok-svc", want: []string{"Bearer tok-svc"}},
		{name: "empty token", token: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var unary, stream []metadata.MD
			conn := dialDecorated(
				t,
				wireBase("probe", fastRetrier(t)),
				tc.token,
				func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn,
					_ grpc.UnaryInvoker, _ ...grpc.CallOption,
				) error {
					md, _ := metadata.FromOutgoingContext(ctx)
					unary = append(unary, md)
					return errCaptured
				},
				func(ctx context.Context, _ *grpc.StreamDesc, _ *grpc.ClientConn, _ string,
					_ grpc.Streamer, _ ...grpc.CallOption,
				) (grpc.ClientStream, error) {
					md, _ := metadata.FromOutgoingContext(ctx)
					stream = append(stream, md)
					return nil, errCaptured
				},
			)

			require.Equal(t, codes.Aborted,
				status.Code(conn.Invoke(ctx, "/test.Service/Unary", nil, nil)))
			_, err := conn.NewStream(ctx, &grpc.StreamDesc{}, "/test.Service/Stream")
			require.Equal(t, codes.Aborted, status.Code(err))

			require.Len(t, unary, 3, "the transient failure is retried")
			require.Len(t, stream, 1, "a stream open is never retried")
			for i, md := range append(unary, stream...) {
				assert.Equal(t, tc.want, md.Get("authorization"), "call %d", i)
				tp := md.Get("traceparent")
				require.Len(t, tp, 1, "call %d", i)
				assert.Contains(t, tp[0], sc.TraceID().String(), "call %d", i)
			}
		})
	}
}

// TestGRPCClientOptions_ClassifyByTheSharedPolicy tests how a decorated gRPC client
// classifies and reports failures.
//
// Why this test is important:
//   - A gRPC status carries no domain code; unless the client codes it, the shared
//     retry policy treats InvalidArgument as transient and replays a call that can
//     never succeed, and the caller must still see the server's own status.
//   - A failure the tier makes itself (an open breaker) must reach the caller as a gRPC
//     status of its code, not a bare Go error that a generated stub reports as Unknown.
//
// What it tests:
//   - InvalidArgument is not retried and returns verbatim (code and message).
//   - Unavailable is retried until it succeeds.
//   - An open breaker rejects without calling the server, as codes.Unavailable.
func TestGRPCClientOptions_ClassifyByTheSharedPolicy(t *testing.T) {
	t.Parallel()
	noStream := func(context.Context, *grpc.StreamDesc, *grpc.ClientConn, string,
		grpc.Streamer, ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		return nil, errCaptured
	}
	answers := func(calls *int, errs ...error) grpc.UnaryClientInterceptor {
		return func(context.Context, string, any, any, *grpc.ClientConn,
			grpc.UnaryInvoker, ...grpc.CallOption,
		) error {
			*calls++
			if *calls <= len(errs) {
				return errs[*calls-1]
			}
			return nil
		}
	}

	t.Run("permanent", func(t *testing.T) {
		t.Parallel()
		calls := 0
		bad := status.Error(codes.InvalidArgument, "bad query")
		conn := dialDecorated(t, wireBase("probe", fastRetrier(t)), "",
			answers(&calls, bad, bad), noStream)
		err := conn.Invoke(context.Background(), "/test.Service/Unary", nil, nil)
		assert.Equal(t, codes.InvalidArgument, status.Code(err))
		assert.Equal(t, "bad query", status.Convert(err).Message())
		assert.Equal(t, 1, calls)
	})

	t.Run("transient", func(t *testing.T) {
		t.Parallel()
		calls := 0
		down := status.Error(codes.Unavailable, "down")
		conn := dialDecorated(t, wireBase("probe", fastRetrier(t)), "",
			answers(&calls, down, down), noStream)
		require.NoError(
			t,
			conn.Invoke(context.Background(), "/test.Service/Unary", nil, nil),
		)
		assert.Equal(t, 3, calls)
	})

	t.Run("open breaker", func(t *testing.T) {
		t.Parallel()
		calls := 0
		conn := dialDecorated(
			t,
			wireBase("probe", decorate.Deps{Breaker: fixtures.StubCircuitBreaker(true)}),
			"",
			answers(&calls),
			noStream,
		)
		err := conn.Invoke(context.Background(), "/test.Service/Unary", nil, nil)
		assert.Equal(t, codes.Unavailable, status.Code(err))
		assert.Zero(t, calls)
	})
}

// sampledSpanContext returns a valid, sampled W3C span context to seed a ctx with,
// so the propagator has an active trace to inject.
func sampledSpanContext() oteltrace.SpanContext {
	return oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID: oteltrace.TraceID{
			0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
			0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
		},
		SpanID:     oteltrace.SpanID{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18},
		TraceFlags: oteltrace.FlagsSampled,
		Remote:     true,
	})
}
