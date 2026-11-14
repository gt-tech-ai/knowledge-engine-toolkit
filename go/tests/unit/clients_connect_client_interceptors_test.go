package unit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc/connect/interceptors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// probeProcedure is the procedure the probe server serves.
const probeProcedure = "/test.v1.Probe/Do"

// probeServer serves probeProcedure over HTTP, answering each call with the next of errs
// (then success) and recording the traceparent it received; calls counts the calls.
func probeServer(
	t *testing.T,
	calls *atomic.Int32,
	traceparent *atomic.Value,
	errs ...error,
) string {
	t.Helper()

	server := connect.NewServer()
	server.Register(connect.Method{
		Spec: probeSpec(connect.IdempotencyUnknown),
		Handler: func(ctx context.Context, _ connect.Spec, stream connect.ServerStream) error {
			call := int(calls.Add(1))

			info, _ := connect.CallInfoForServerContext(ctx)
			traceparent.Store(info.RequestHeader().Get("Traceparent"))

			err := stream.Receive(new(emptypb.Empty))
			if err != nil {
				return connect.NewError(connect.CodeInternal, "receive the probe").
					WithCause(err)
			}

			if call <= len(errs) {
				return errs[call-1]
			}

			return stream.Send(new(emptypb.Empty))
		},
	})

	mux := http.NewServeMux()
	connecthttp.Mount(mux, server)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv.URL
}

// probeSpec is probeProcedure's unary spec at the declared idempotency level.
func probeSpec(level connect.IdempotencyLevel) connect.Spec {
	return connect.Spec{
		StreamType:       connect.StreamTypeUnary,
		IdempotencyLevel: level,
		Schema:           nil,
		Procedure:        probeProcedure,
	}
}

// probeCall calls probeProcedure at url through a Connect client decorated by base,
// whose schema declares the procedure free of side effects (so the tier may replay it).
func probeCall(ctx context.Context, url string, base *client.Base) error {
	return probeCallAt(ctx, url, base, connect.IdempotencyNoSideEffects)
}

// probeCallAt is probeCall with the procedure's declared idempotency level.
func probeCallAt(
	ctx context.Context,
	url string,
	base *client.Base,
	level connect.IdempotencyLevel,
) error {
	rpc := connect.NewClient(
		connecthttp.NewTransport(http.DefaultClient, url),
		interceptors.ClientInterceptors(base)...,
	)

	return apperr.Wrap(
		rpc.CallUnary(ctx, probeSpec(level), new(emptypb.Empty), new(emptypb.Empty)),
		apperr.CodeUnknown,
		"call the probe",
	)
}

// TestConnectClientInterceptors_DecorateEveryCall tests a decorated Connect client.
//
// Why this test is important:
//   - A Connect client must classify a failure by the shared retry policy — replaying
//     an Unavailable, never an InvalidArgument — and hand the caller the server's own
//     error; a tier-made failure (an open breaker) must still be a *connect.Error.
//   - Without the traceparent on the wire the server starts a disconnected trace.
//
// What it tests:
//   - Unavailable twice then success: three calls, success, and the server received the
//     active trace's traceparent.
//   - Unavailable on a procedure of unknown idempotency (a write that may have landed):
//     one call, and the caller gets CodeUnavailable — it is never replayed.
//   - InvalidArgument: one call, and the caller gets CodeInvalidArgument with the
//     server's message.
//   - An open breaker: no call, CodeUnavailable.
func TestConnectClientInterceptors_DecorateEveryCall(t *testing.T) {
	t.Parallel()

	otel.SetTextMapPropagator(propagation.TraceContext{})

	spanContext := sampledSpanContext()
	ctx := oteltrace.ContextWithSpanContext(context.Background(), spanContext)
	down := connect.NewError(connect.CodeUnavailable, "")

	t.Run("transient", func(t *testing.T) {
		t.Parallel()

		var (
			calls atomic.Int32
			value atomic.Value
		)

		url := probeServer(t, &calls, &value, down, down)
		err := probeCall(ctx, url, new(wireBase(textProbe, new(fastRetrier(t)))))
		require.NoError(t, err)
		assert.EqualValues(t, 3, calls.Load())
		assert.Contains(t, value.Load(), spanContext.TraceID().String())
	})

	t.Run("non-idempotent", func(t *testing.T) {
		t.Parallel()

		var (
			calls atomic.Int32
			tp    atomic.Value
		)

		url := probeServer(t, &calls, &tp, down, down)
		err := probeCallAt(
			ctx,
			url,
			new(wireBase(textProbe, new(fastRetrier(t)))),
			connect.IdempotencyUnknown,
		)
		assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
		assert.EqualValues(t, 1, calls.Load())
	})

	testConnectClientInterceptorsPermanentError(ctx, t)
}

// testConnectClientInterceptorsPermanentError continues
// TestConnectClientInterceptors_DecorateEveryCall: permanent error.
func testConnectClientInterceptorsPermanentError(
	ctx context.Context,
	t *testing.T,
) {
	t.Helper()

	t.Run("permanent", func(t *testing.T) {
		t.Parallel()

		var (
			calls atomic.Int32
			tp    atomic.Value
		)

		bad := connect.NewError(connect.CodeInvalidArgument, "bad query")
		url := probeServer(t, &calls, &tp, bad, bad)
		err := probeCall(ctx, url, new(wireBase(textProbe, new(fastRetrier(t)))))
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		assert.Contains(t, err.Error(), "bad query")
		assert.EqualValues(t, 1, calls.Load())
	})

	t.Run("open breaker", func(t *testing.T) {
		t.Parallel()

		var (
			calls atomic.Int32
			tp    atomic.Value
		)

		url := probeServer(t, &calls, &tp)
		base := wireBase(
			textProbe,
			fixtures.BuildRef(func(row *decorate.Deps) {
				row.Breaker = fixtures.OpenCircuitBreaker()
			}),
		)
		err := probeCall(ctx, url, &base)
		assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
		assert.Zero(t, calls.Load())
	})
}
