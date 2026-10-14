package unit_test

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/connect/interceptors"
	grpcserver "github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/grpc"
	grpcinterceptors "github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/grpc/interceptors"
)

// runGroup is one run of the controller tier's generic layers, as transport.Interceptors
// hands it to adapt.
type runGroup struct {
	// unary and stream are the run's middleware for a unary call and a stream.
	unary, stream decorate.OpMiddleware
}

// TestInterceptors_LaysOutTheControllerTier tests transport.Interceptors.
//
// Why this test is important:
//   - Every RPC server lays out its interceptors through it, so the controller tier's
//     order, the caller's authentication and the stream timeout exemption are decided
//     once for every framework.
//
// What it tests:
//   - Native layers keep the tier's position; consecutive generic layers become one
//     run; authn sits just before the auth layer.
//   - A run's unary middleware enforces the timeout; its stream middleware does not.
func TestInterceptors_LaysOutTheControllerTier(t *testing.T) {
	t.Parallel()
	var runs []runGroup
	native := func(layer decorate.Layer) ([]string, bool) {
		switch layer {
		case decorate.LayerTracing:
			return []string{"tracing"}, true
		case decorate.LayerMetrics:
			return []string{"metrics"}, true
		case decorate.LayerLogging:
			return nil, true // the collaborator is absent
		case decorate.LayerRecovery,
			decorate.LayerAuth,
			decorate.LayerTimeout,
			decorate.LayerCircuitBreaker,
			decorate.LayerRetry,
			decorate.LayerBulkhead,
			decorate.LayerRateLimit:
			return nil, false
		default:
			return nil, false
		}
	}
	adapt := func(unary, stream decorate.OpMiddleware) string {
		runs = append(runs, runGroup{unary: unary, stream: stream})
		return "run"
	}
	d := decorate.Deps{
		RateLimiter: fixtures.StubRateLimiter(true),
		Auth:        func(context.Context, string) error { return nil },
		Timeout:     5 * time.Millisecond,
	}

	got := transport.Interceptors("svc", d, native, adapt, []string{"authn"})

	assert.Equal(t, []string{"tracing", "metrics", "run", "authn", "run"}, got)
	require.Len(t, runs, 2)
	slow := func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
			return nil
		}
	}
	err := runs[1].unary.WrapOp(t.Context(), "op", slow)
	assert.Equal(t, apperr.CodeTimeout, apperr.Code(err))
	assert.NoError(t, runs[1].stream.WrapOp(t.Context(), "op", slow))
}

// optionsProcedure is the procedure the ServerOptions tests serve.
const optionsProcedure = "/test.v1.Probe/Do"

// serveUnary serves one unary Connect call to optionsProcedure, handled by fn through a
// handler built with opts, in process (no socket), and returns the HTTP status.
func serveUnary(
	ctx context.Context,
	opts []connect.HandlerOption,
	fn func(context.Context) error,
) int {
	handler := connect.NewUnaryHandler(
		optionsProcedure,
		func(
			ctx context.Context,
			_ *connect.Request[probeMsg],
		) (*connect.Response[probeMsg], error) {
			if err := fn(ctx); err != nil {
				return nil, err
			}
			return connect.NewResponse(&probeMsg{}), nil
		},
		append(opts, connect.WithCodec(probeCodec{}))...)
	req := httptest.NewRequestWithContext(
		ctx,
		http.MethodPost,
		optionsProcedure,
		http.NoBody,
	)
	req.Header.Set("Content-Type", "application/proto")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code
}

// serveStream serves one server-streaming Connect call to optionsProcedure, handled by fn
// through a handler built with opts, in process, and returns the response body (the
// end-of-stream message carries any error).
func serveStream(
	ctx context.Context,
	opts []connect.HandlerOption,
	fn func(context.Context) error,
) string {
	handler := connect.NewServerStreamHandler(
		optionsProcedure,
		func(
			ctx context.Context,
			_ *connect.Request[probeMsg],
			_ *connect.ServerStream[probeMsg],
		) error {
			return fn(ctx)
		},
		append(opts, connect.WithCodec(probeCodec{}))...)
	// One enveloped, empty request message: flags 0, length 0.
	body := bytes.NewReader([]byte{0, 0, 0, 0, 0})
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, optionsProcedure, body)
	req.Header.Set("Content-Type", "application/connect+proto")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Body.String()
}

// TestConnectServerOptions_DecoratesEveryRPC tests interceptors.ServerOptions.
//
// Why this test is important:
//   - A Connect server must reject excess load, contain a panic and authorize each RPC
//     exactly as the controllers tier of the decoration config says, while a handler's
//     own error reaches the caller untouched.
//
// What it tests:
//   - No layers (recovery switched off) and no edge interceptors yield no options.
//   - A denied rate limit is 429 (resource exhausted), a panic 500 (internal), a
//     denial 403, and a handler's not-found 404.
//   - Auth sees "<controller>.<procedure>"; a denial's own text never reaches the
//     caller, only the code's client-safe message.
//   - A stream is rate limited too, a panic in a stream handler is an internal error
//     rather than a dropped connection, and a stream outlives the timeout.
func TestConnectServerOptions_DecoratesEveryRPC(t *testing.T) {
	t.Parallel()
	ok := func(context.Context) error { return nil }
	assert.Nil(t, interceptors.ServerOptions(
		"probe", decorate.Deps{SkipRecovery: true}, interceptors.Edge{},
	))

	limited := interceptors.ServerOptions("probe", decorate.Deps{
		RateLimiter: fixtures.StubRateLimiter(false),
	}, interceptors.Edge{})
	assert.Equal(t, http.StatusTooManyRequests, serveUnary(t.Context(), limited, ok))
	assert.Contains(t, serveStream(t.Context(), limited, ok), "resource_exhausted")

	var actions []string
	guarded := interceptors.ServerOptions("probe", decorate.Deps{
		Auth: func(_ context.Context, action string) error {
			actions = append(actions, action)
			if len(actions) > 1 {
				return apperr.New(apperr.CodeForbidden, "denied: secret-action-detail")
			}
			return nil
		},
		Timeout: 5 * time.Millisecond,
	}, interceptors.Edge{})
	assert.Equal(t, http.StatusOK, serveUnary(t.Context(), guarded, ok))
	assert.Equal(t, []string{"probe.test.v1.Probe/Do"}, actions)
	assert.Equal(t, http.StatusForbidden, serveUnary(t.Context(), guarded, ok))
	denied := serveStream(t.Context(), guarded, ok)
	assert.Contains(t, denied, "permission denied")
	assert.NotContains(t, denied, "secret-action-detail")

	panicking := func(context.Context) error { panic("boom") }
	recovering := interceptors.ServerOptions(
		"probe",
		decorate.Deps{},
		interceptors.Edge{},
	)
	assert.Equal(
		t,
		http.StatusInternalServerError,
		serveUnary(t.Context(), recovering, panicking),
	)
	assert.Contains(t, serveStream(t.Context(), recovering, panicking), "internal")

	notFound := func(context.Context) error {
		return connect.NewError(connect.CodeNotFound, apperr.Sentinel("no such probe"))
	}
	bounded := interceptors.ServerOptions("probe", decorate.Deps{
		Timeout: 5 * time.Millisecond,
	}, interceptors.Edge{})
	assert.Equal(t, http.StatusNotFound, serveUnary(t.Context(), bounded, notFound))
	slow := func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(30 * time.Millisecond):
			return nil
		}
	}
	assert.NotContains(t, serveStream(t.Context(), bounded, slow), "error")
}

// TestGRPCServerOptions_DecoratesEveryRPC tests grpc interceptors.ServerOptions.
//
// Why this test is important:
//   - A gRPC server must be decorated by the same controllers tier as a Connect one, for
//     unary and streaming RPCs alike, and must never send a rejection's internal text.
//
// What it tests:
//   - No layers (recovery switched off) yield no options.
//   - Through a served health Check (unary) and Watch (stream, unimplemented by the
//     KE health server but still decorated), auth sees
//     "<controller>.<method>", a denied rate limit is RESOURCE_EXHAUSTED, and a denial
//     is PERMISSION_DENIED carrying only "permission denied".
func TestGRPCServerOptions_DecoratesEveryRPC(t *testing.T) {
	t.Parallel()
	assert.Nil(
		t,
		grpcinterceptors.ServerOptions(
			"health", decorate.Deps{SkipRecovery: true}, grpcinterceptors.Edge{},
		),
	)

	var actions []string
	serve := func(d decorate.Deps) grpc_health_v1.HealthClient {
		srv := grpcserver.NewServer(
			grpcserver.DefaultServerConfig(),
			grpcinterceptors.ServerOptions("health", d, grpcinterceptors.Edge{})...,
		)
		grpcserver.RegisterHealthService(srv)
		lis := bufconn.Listen(1 << 20)
		go func() { _ = srv.Serve(lis) }()
		t.Cleanup(srv.Stop)
		conn, err := grpc.NewClient(
			"passthrough:///bufnet",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return lis.DialContext(ctx)
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })
		return grpc_health_v1.NewHealthClient(conn)
	}
	check := func(d decorate.Deps) error {
		_, err := serve(d).Check(t.Context(), &grpc_health_v1.HealthCheckRequest{})
		return err //nolint:wrapcheck // the test reads the status off the RPC's own error
	}
	watch := func(d decorate.Deps) error {
		stream, err := serve(d).Watch(t.Context(), &grpc_health_v1.HealthCheckRequest{})
		require.NoError(t, err)
		_, err = stream.Recv()
		return err //nolint:wrapcheck // the test reads the status off the RPC's own error
	}
	allow := decorate.Deps{Auth: func(_ context.Context, action string) error {
		actions = append(actions, action)
		return nil
	}}
	deny := decorate.Deps{Auth: func(context.Context, string) error {
		return apperr.New(apperr.CodeForbidden, "denied: secret-action-detail")
	}}
	limited := decorate.Deps{RateLimiter: fixtures.StubRateLimiter(false)}

	require.NoError(t, check(allow))
	// The KE health server leaves Watch unimplemented; the stream still runs the tier.
	assert.Equal(t, codes.Unimplemented, status.Code(watch(allow)))
	assert.Equal(t, []string{
		"health.grpc.health.v1.Health/Check",
		"health.grpc.health.v1.Health/Watch",
	}, actions)
	for _, rpc := range []func(decorate.Deps) error{check, watch} {
		assert.Equal(t, codes.ResourceExhausted, status.Code(rpc(limited)))
		denied := status.Convert(rpc(deny))
		assert.Equal(t, codes.PermissionDenied, denied.Code())
		assert.Equal(t, "permission denied", denied.Message())
	}
}
