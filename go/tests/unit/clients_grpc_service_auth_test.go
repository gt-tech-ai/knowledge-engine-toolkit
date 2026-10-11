package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	grpcinterceptors "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc/grpc/interceptors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// TestServiceAuthClientInterceptors_AttachBearer tests that the unary and stream
// service-auth interceptors attach the caller's service-to-service token as
// `authorization: Bearer <token>` outgoing metadata, and attach nothing when no token
// is configured (dev bypass).
//
// Why this test is important:
//   - A server enforcing service auth rejects a caller without the token, so the client
//     must SEND it on unary calls and stream opens alike.
//   - An empty token must attach no header so a local-dev server bypass keeps working;
//     the builders skip an empty token themselves, so only a direct call proves each
//     interceptor's own documented empty-token behaviour.
//
// What it tests:
//   - For both ServiceAuthClientInterceptor and ServiceAuthStreamClientInterceptor, a
//     non-empty token → outgoing metadata carries "Bearer <token>"; empty → none.
func TestServiceAuthClientInterceptors_AttachBearer(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		token string
		want  string
	}{
		{name: "configured token", token: "tok-svc", want: "Bearer tok-svc"},
		{name: "empty token", token: "", want: ""},
	}
	for _, tc := range cases {
		t.Run("unary: "+tc.name, func(t *testing.T) {
			t.Parallel()
			var got string
			inv := func(
				ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption,
			) error {
				got = bearerOf(ctx)
				return nil
			}
			err := grpcinterceptors.ServiceAuthClientInterceptor(tc.token)(
				context.Background(), "/test.v1.Probe/Do", nil, nil, nil, inv,
			)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
		t.Run("stream: "+tc.name, func(t *testing.T) {
			t.Parallel()
			var got string
			streamer := func(
				ctx context.Context, _ *grpc.StreamDesc, _ *grpc.ClientConn, _ string,
				_ ...grpc.CallOption,
			) (grpc.ClientStream, error) {
				got = bearerOf(ctx)
				return fixtures.StubClientStream(ctx, nil), nil
			}
			_, err := grpcinterceptors.ServiceAuthStreamClientInterceptor(tc.token)(
				context.Background(),
				&grpc.StreamDesc{},
				nil,
				"/test.v1.Probe/Watch",
				streamer,
			)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// errCaptured stops a captured call after its outgoing metadata is recorded.
var errCaptured = status.Error(codes.Aborted, "captured")

// bearerOf returns the first outgoing `authorization` metadata value on ctx ("" if none).
func bearerOf(ctx context.Context) string {
	if vals := bearersOf(ctx); len(vals) > 0 {
		return vals[0]
	}
	return ""
}

// bearersOf returns every outgoing `authorization` metadata value on ctx.
func bearersOf(ctx context.Context) []string {
	md, _ := metadata.FromOutgoingContext(ctx)
	return md.Get("authorization")
}

// TestGRPCStreamingClientBuilder_WithServiceAuth_AuthenticatesEveryRPC tests that a
// streaming client built with a service token presents it on every RPC of the
// connection — stream opens and unary calls alike.
//
// Why this test is important:
//   - A streaming client (e.g. a server stream plus a unary side call on one conn)
//     calling an internal server that enforces service auth is rejected on any RPC
//     that lacks the bearer token, so the token must ride both stream and unary calls.
//   - An empty token must attach nothing so a local-dev server bypass keeps working.
//
// What it tests:
//   - With WithServiceAuth("tok-svc"), a stream open and a unary call on a conn built
//     from Build()'s options both carry "Bearer tok-svc"; with an empty token Build
//     returns no options and nothing is attached.
func TestGRPCStreamingClientBuilder_WithServiceAuth_AuthenticatesEveryRPC(t *testing.T) {
	t.Parallel()

	var streamBearer, unaryBearer string
	captureStream := func(
		ctx context.Context, _ *grpc.StreamDesc, _ *grpc.ClientConn, _ string,
		_ grpc.Streamer, _ ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		streamBearer = bearerOf(ctx)
		return nil, errCaptured
	}
	captureUnary := func(
		ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn,
		_ grpc.UnaryInvoker, _ ...grpc.CallOption,
	) error {
		unaryBearer = bearerOf(ctx)
		return errCaptured
	}

	opts := grpcinterceptors.NewStreamingClientBuilder().
		WithServiceAuth("tok-svc").
		Build()
	conn, err := grpc.NewClient(
		"passthrough:///streaming-service-auth-test",
		append(opts,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithChainStreamInterceptor(captureStream),
			grpc.WithChainUnaryInterceptor(captureUnary),
		)...,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	_, err = conn.NewStream(
		context.Background(),
		&grpc.StreamDesc{},
		"/test.Service/Stream",
	)
	require.ErrorIs(t, err, errCaptured)
	require.ErrorIs(
		t,
		conn.Invoke(context.Background(), "/test.Service/Unary", nil, nil),
		errCaptured,
	)

	assert.Equal(t, "Bearer tok-svc", streamBearer, "the stream open carries the token")
	assert.Equal(t, "Bearer tok-svc", unaryBearer, "the unary call carries the token")
	assert.Nil(
		t,
		grpcinterceptors.NewStreamingClientBuilder().WithServiceAuth("").Build(),
	)
}

// TestGRPCClientBuilders_ServiceAuth_OneBearerPerRPC tests that service auth presents
// exactly one bearer token on every attempt: on each retried stream open, and on unary
// calls of a conn that combines ClientBuilder and StreamingClientBuilder options.
//
// Why this test is important:
//   - Service auth sits innermost in the stream chain, so a retried open must carry the
//     token on every attempt; moving it outside retry would send it only once.
//   - grpc.WithChainUnaryInterceptor appends across dial options, so a conn built from
//     both builders runs two unary service-auth interceptors. `authorization` is not a
//     list header (RFC 7235); a server or proxy that enforces one value rejects a call
//     that carries two.
//
// What it tests:
//   - With WithRetry and WithServiceAuth, each of the three stream-open attempts carries
//     exactly one "Bearer tok-svc".
//   - A unary call on a conn built from both builders' options carries exactly one
//     authorization value.
func TestGRPCClientBuilders_ServiceAuth_OneBearerPerRPC(t *testing.T) {
	t.Parallel()

	t.Run("every retried stream open carries the token", func(t *testing.T) {
		t.Parallel()
		retrier, _ := fixtures.StubRetrier(2, false)
		var perAttempt [][]string
		captureStream := func(
			ctx context.Context, _ *grpc.StreamDesc, _ *grpc.ClientConn, _ string,
			_ grpc.Streamer, _ ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			perAttempt = append(perAttempt, bearersOf(ctx))
			return nil, errCaptured // Aborted is retryable
		}
		opts := grpcinterceptors.NewStreamingClientBuilder().
			WithRetry(retrier).
			WithServiceAuth("tok-svc").
			Build()
		conn, err := grpc.NewClient(
			"passthrough:///streaming-service-auth-retry-test",
			append(opts,
				grpc.WithTransportCredentials(insecure.NewCredentials()),
				grpc.WithChainStreamInterceptor(captureStream),
			)...,
		)
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })

		_, err = conn.NewStream(
			context.Background(),
			&grpc.StreamDesc{},
			"/test.Service/Stream",
		)
		require.Error(t, err)
		require.Len(t, perAttempt, 3, "the retrier makes three attempts")
		for i, got := range perAttempt {
			assert.Equal(t, []string{"Bearer tok-svc"}, got, "attempt %d", i+1)
		}
	})

	t.Run("a conn built from both builders stamps unary calls once", func(t *testing.T) {
		t.Parallel()
		var got []string
		captureUnary := func(
			ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn,
			_ grpc.UnaryInvoker, _ ...grpc.CallOption,
		) error {
			got = bearersOf(ctx)
			return errCaptured
		}
		opts := append(
			grpcinterceptors.NewClientBuilder().WithServiceAuth("tok-svc").Build(),
			grpcinterceptors.NewStreamingClientBuilder().
				WithServiceAuth("tok-svc").
				Build()...,
		)
		conn, err := grpc.NewClient(
			"passthrough:///combined-service-auth-test",
			append(opts,
				grpc.WithTransportCredentials(insecure.NewCredentials()),
				grpc.WithChainUnaryInterceptor(captureUnary),
			)...,
		)
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })

		require.ErrorIs(
			t,
			conn.Invoke(context.Background(), "/test.Service/Unary", nil, nil),
			errCaptured,
		)
		assert.Equal(t, []string{"Bearer tok-svc"}, got)
	})
}
