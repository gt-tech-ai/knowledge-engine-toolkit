package unit_test

import (
	"context"
	"net"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
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
			return []string{textTracing}, true
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
			// Any other value takes the shared result below.
		}

		return nil, false
	}
	adapt := func(unary, stream decorate.OpMiddleware) string {
		runs = append(runs, runGroup{unary: unary, stream: stream})

		return textRun
	}
	deps := fixtures.Build(func(row *decorate.Deps) {
		row.RateLimiter = fixtures.AllowingRateLimiter()
		row.Auth = func(context.Context, string) error { return nil }
		row.Timeout = timeout * time.Millisecond
	})

	got := transport.Interceptors(textSvc, &deps, native, adapt, []string{"authn"})

	assert.Equal(t, []string{textTracing, "metrics", textRun, "authn", textRun}, got)
	require.Len(t, runs, wantRunsLen)

	slow := func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
			return nil
		}
	}
	err := runs[1].unary.WrapOp(t.Context(), textOp, slow)
	assert.Equal(t, apperr.CodeTimeout, apperr.Code(err))
	assert.NoError(t, runs[1].stream.WrapOp(t.Context(), textOp, slow))
}

// serveUnary dispatches one unary call through a connect.Server built with ics, handled
// by callback, and returns the call's error.
func serveUnary(
	ctx context.Context,
	ics []connect.ServerInterceptor,
	callback func(context.Context) error,
) error {
	return serveRPC(ctx, ics, newTestRequest(), callback)
}

// serveStream dispatches one server-streaming call through a connect.Server built with
// ics, handled by callback, and returns the call's error.
func serveStream(
	ctx context.Context,
	ics []connect.ServerInterceptor,
	callback func(context.Context) error,
) error {
	return serveRPC(ctx, ics, newTestStream(nil), callback)
}

// TestConnectServerInterceptors_DecoratesEveryRPC tests interceptors.ServerInterceptors.
//
// Why this test is important:
//   - A Connect server must reject excess load, contain a panic and authorize each RPC
//     exactly as the controllers tier of the decoration config says, while a handler's
//     own error reaches the caller untouched.
//
// What it tests:
//   - No layers (recovery switched off) and no edge interceptors yield no interceptors.
//   - A denied rate limit is resource exhausted, a panic internal, a denial permission
//     denied, and a handler's not-found stays not found.
//   - Auth sees "<controller>.<procedure>"; a denial's own text never reaches the
//     caller, only the code's client-safe message.
//   - A stream is rate limited too, a panic in a stream handler is an internal error
//     rather than a dropped connection, and a stream outlives the timeout.
func TestConnectServerInterceptors_DecoratesEveryRPC(t *testing.T) {
	t.Parallel()

	callback := func(context.Context) error { return nil }

	assert.Empty(t, interceptors.ServerInterceptors(
		textProbe,
		fixtures.BuildRef(func(row *decorate.Deps) {
			row.SkipRecovery = true
		}),
		stdx.Zero[interceptors.Edge](),
	))

	limited := interceptors.ServerInterceptors(
		textProbe,
		fixtures.BuildRef(func(row *decorate.Deps) {
			row.RateLimiter = fixtures.DenyingRateLimiter()
		}),
		stdx.Zero[interceptors.Edge](),
	)
	assertCode(
		t,
		connect.CodeResourceExhausted,
		serveUnary(t.Context(), limited, callback),
	)
	assertCode(
		t,
		connect.CodeResourceExhausted,
		serveStream(t.Context(), limited, callback),
	)

	var actions []string

	guarded := guardedInterceptors(&actions)
	require.NoError(t, serveUnary(t.Context(), guarded, callback))
	assert.Equal(t, []string{"probe.test.v1.TestService/Do"}, actions)
	assertCode(
		t,
		connect.CodePermissionDenied,
		serveUnary(t.Context(), guarded, callback),
	)

	denied := serveStream(t.Context(), guarded, callback)
	assertCode(t, connect.CodePermissionDenied, denied)

	testConnectServerInterceptorsDeniedAndPanics(t, denied)
}

// guardedInterceptors returns server interceptors whose auth layer records each action
// in actions and denies every call after the first, with a detail the caller must
// never see.
func guardedInterceptors(actions *[]string) []connect.ServerInterceptor {
	return interceptors.ServerInterceptors(
		textProbe,
		fixtures.BuildRef(func(row *decorate.Deps) {
			row.Auth = func(_ context.Context, action string) error {
				*actions = append(*actions, action)
				if len(*actions) > 1 {
					return apperr.New(
						apperr.CodeForbidden,
						"denied: secret-action-detail",
					)
				}

				return nil
			}
			row.Timeout = 5 * time.Millisecond
		}),
		stdx.Zero[interceptors.Edge](),
	)
}

// testConnectServerInterceptorsDeniedAndPanics continues
// TestConnectServerInterceptors_DecoratesEveryRPC: denied and panics.
func testConnectServerInterceptorsDeniedAndPanics(t *testing.T, denied error) {
	t.Helper()

	assert.NotContains(t, denied.Error(), "secret-action-detail")

	panicking := func(context.Context) error { panic(textBoom) }
	recovering := interceptors.ServerInterceptors(
		textProbe,
		new(decorate.Deps),
		stdx.Zero[interceptors.Edge](),
	)
	assertCode(t, connect.CodeInternal, serveUnary(t.Context(), recovering, panicking))
	assertCode(t, connect.CodeInternal, serveStream(t.Context(), recovering, panicking))

	notFound := func(context.Context) error {
		return connect.NewError(connect.CodeNotFound, "no such probe")
	}
	bounded := interceptors.ServerInterceptors(
		textProbe,
		fixtures.BuildRef(func(row *decorate.Deps) {
			row.Timeout = 5 * time.Millisecond
		}),
		stdx.Zero[interceptors.Edge](),
	)
	assertCode(t, connect.CodeNotFound, serveUnary(t.Context(), bounded, notFound))

	slow := func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(30 * time.Millisecond):
			return nil
		}
	}
	assert.NoError(t, serveStream(t.Context(), bounded, slow))
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
			"health",
			fixtures.BuildRef(func(row *decorate.Deps) {
				row.SkipRecovery = true
			}),
			stdx.Zero[grpcinterceptors.Edge](),
		),
	)

	var actions []string

	serve := func(d decorate.Deps) grpc_health_v1.HealthClient {
		srv := grpcserver.NewServer(
			grpcserver.DefaultServerConfig(),
			grpcinterceptors.ServerOptions(
				"health",
				&d,
				stdx.Zero[grpcinterceptors.Edge](),
			)...,
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

	testGRPCServerOptionsDecoratesEveryRPCChecksCalls(t, actions, serve)
}

// testGRPCServerOptionsDecoratesEveryRPCChecksCalls continues
// TestGRPCServerOptions_DecoratesEveryRPC: checks calls.
func testGRPCServerOptionsDecoratesEveryRPCChecksCalls(
	t *testing.T,
	actions []string,
	serve func(d decorate.Deps) grpc_health_v1.HealthClient,
) {
	t.Helper()

	check := func(d decorate.Deps) *status.Status {
		_, err := serve(d).Check(t.Context(), new(grpc_health_v1.HealthCheckRequest))

		return status.Convert(err)
	}
	watch := func(d decorate.Deps) *status.Status {
		stream, err := serve(d).Watch(t.Context(), new(grpc_health_v1.HealthCheckRequest))
		require.NoError(t, err)

		_, err = stream.Recv()

		return status.Convert(err)
	}
	allow := fixtures.Build(func(row *decorate.Deps) {
		row.Auth = func(_ context.Context, action string) error {
			actions = append(actions, action)

			return nil
		}
	})
	deny := fixtures.Build(func(row *decorate.Deps) {
		row.Auth = func(context.Context, string) error {
			return apperr.New(apperr.CodeForbidden, "denied: secret-action-detail")
		}
	})
	limited := fixtures.Build(func(row *decorate.Deps) {
		row.RateLimiter = fixtures.DenyingRateLimiter()
	})

	assert.Equal(t, codes.OK, check(allow).Code())
	// The KE health server leaves Watch unimplemented; the stream still runs the tier.
	assert.Equal(t, codes.Unimplemented, watch(allow).Code())
	assert.Equal(t, []string{
		"health.grpc.health.v1.Health/Check",
		"health.grpc.health.v1.Health/Watch",
	}, actions)

	for _, rpc := range []func(decorate.Deps) *status.Status{check, watch} {
		assert.Equal(t, codes.ResourceExhausted, rpc(limited).Code())

		denied := rpc(deny)
		assert.Equal(t, codes.PermissionDenied, denied.Code())
		assert.Equal(t, "permission denied", denied.Message())
	}
}

const (
	// timeout is the factor multiplying time.Millisecond in
	// TestInterceptors_LaysOutTheControllerTier.
	timeout = 5
)
