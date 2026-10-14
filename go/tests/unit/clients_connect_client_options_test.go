package unit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc/connect/interceptors"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// probeProcedure is the procedure the probe server serves.
const probeProcedure = "/test.v1.Probe/Do"

// probeServer serves probeProcedure, answering each call with the next of errs (then
// success) and recording the traceparent it received; calls counts the calls.
func probeServer(
	t *testing.T,
	calls *atomic.Int32,
	traceparent *atomic.Value,
	errs ...error,
) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(probeProcedure, connect.NewUnaryHandler(probeProcedure, func(
		_ context.Context, req *connect.Request[emptypb.Empty],
	) (*connect.Response[emptypb.Empty], error) {
		n := int(calls.Add(1))
		traceparent.Store(req.Header().Get("traceparent"))
		if n <= len(errs) {
			return nil, errs[n-1]
		}
		return connect.NewResponse(&emptypb.Empty{}), nil
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// probeClient is a Connect client of probeProcedure decorated by base.
func probeClient(
	url string,
	base client.Base,
) *connect.Client[emptypb.Empty, emptypb.Empty] {
	return connect.NewClient[emptypb.Empty, emptypb.Empty](
		http.DefaultClient, url+probeProcedure, interceptors.ClientOptions(base)...,
	)
}

// TestConnectClientOptions_DecorateEveryCall tests a decorated Connect client.
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
//   - InvalidArgument: one call, and the caller gets CodeInvalidArgument with the
//     server's message.
//   - An open breaker: no call, CodeUnavailable.
func TestConnectClientOptions_DecorateEveryCall(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	sc := sampledSpanContext()
	ctx := oteltrace.ContextWithSpanContext(context.Background(), sc)
	down := connect.NewError(connect.CodeUnavailable, nil)

	t.Run("transient", func(t *testing.T) {
		var calls atomic.Int32
		var tp atomic.Value
		url := probeServer(t, &calls, &tp, down, down)
		_, err := probeClient(url, client.NewBase("probe", fastRetrier(t))).
			CallUnary(ctx, connect.NewRequest(&emptypb.Empty{}))
		require.NoError(t, err)
		assert.EqualValues(t, 3, calls.Load())
		assert.Contains(t, tp.Load(), sc.TraceID().String())
	})

	t.Run("permanent", func(t *testing.T) {
		var calls atomic.Int32
		var tp atomic.Value
		bad := connect.NewError(connect.CodeInvalidArgument, apperr.Sentinel("bad query"))
		url := probeServer(t, &calls, &tp, bad, bad)
		_, err := probeClient(url, client.NewBase("probe", fastRetrier(t))).
			CallUnary(ctx, connect.NewRequest(&emptypb.Empty{}))
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		assert.Contains(t, err.Error(), "bad query")
		assert.EqualValues(t, 1, calls.Load())
	})

	t.Run("open breaker", func(t *testing.T) {
		var calls atomic.Int32
		var tp atomic.Value
		url := probeServer(t, &calls, &tp)
		base := client.NewBase(
			"probe",
			decorate.Deps{Breaker: fixtures.StubCircuitBreaker(true)},
		)
		_, err := probeClient(
			url,
			base,
		).CallUnary(ctx, connect.NewRequest(&emptypb.Empty{}))
		assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
		assert.Zero(t, calls.Load())
	})
}
