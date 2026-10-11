package unit_test

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/transport/connect/interceptors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestStaticTokenValidator_PerCaller tests that the static validator resolves a bearer
// token to the calling service's identity and rejects unknown/empty tokens.
//
// Why this test is important:
//   - The per-caller map is what gives the internal surface an audited
//     caller identity + least-privilege — a validator that returned only a bool would
//     lose the "which service called" signal the design exists to provide
//   - An empty or unknown token must never resolve to a caller (fail-closed)
//
// What it tests:
//   - a known token → (caller, true); a wrong token → ("", false); "" → ("", false)
func TestStaticTokenValidator_PerCaller(t *testing.T) {
	t.Parallel()
	v := interceptors.NewStaticTokenValidator(map[string]string{
		"reports": "tok-reports",
		"orders":  "tok-orders",
	})

	caller, ok := v.Validate("tok-reports")
	assert.True(t, ok)
	assert.Equal(t, "reports", caller)

	caller, ok = v.Validate("tok-orders")
	assert.True(t, ok)
	assert.Equal(t, "orders", caller)

	_, ok = v.Validate("tok-wrong")
	assert.False(t, ok, "an unknown token must not resolve to any caller")

	_, ok = v.Validate("")
	assert.False(t, ok, "an empty token must never match")
}

// TestStaticTokenValidator_DualTokenWindow tests that a comma-separated
// {current,previous} map value accepts BOTH tokens, enabling a zero-downtime rotation
// window.
//
// Why this test is important:
//   - rotates service tokens via a dual-acceptance window; if only one value were
//     honored, rotating a caller's token would fail-closed every in-flight caller until
//     every client had rolled — a self-inflicted outage
//
// What it tests:
//   - both the current and the previous token resolve to the same caller; an unrelated
//     token still fails
func TestStaticTokenValidator_DualTokenWindow(t *testing.T) {
	t.Parallel()
	v := interceptors.NewStaticTokenValidator(map[string]string{
		"reports": "tok-new, tok-old",
	})

	caller, ok := v.Validate("tok-new")
	require.True(t, ok)
	assert.Equal(t, "reports", caller)

	caller, ok = v.Validate("tok-old")
	require.True(t, ok, "the previous token must still be accepted during rotation")
	assert.Equal(t, "reports", caller)

	_, ok = v.Validate("tok-unrelated")
	assert.False(t, ok)
}

// TestStaticTokenValidator_EmptyMapRejectsAll tests that a validator built from an empty
// (or all-blank) token map resolves nothing.
//
// Why this test is important:
//   - The interceptor decides dev-bypass via the stub flag, NOT via an empty map; the
//     validator itself must be strict so a mis-wired enforce-mode server with no tokens
//     rejects every caller rather than silently admitting them
//
// What it tests:
//   - an empty map and a blank-value map both yield no match for any token
func TestStaticTokenValidator_EmptyMapRejectsAll(t *testing.T) {
	t.Parallel()
	empty := interceptors.NewStaticTokenValidator(nil)
	_, ok := empty.Validate("anything")
	assert.False(t, ok)

	blank := interceptors.NewStaticTokenValidator(map[string]string{"reports": "  , "})
	_, ok = blank.Validate("anything")
	assert.False(t, ok, "a caller with only blank tokens must be dropped")
}

// Compile-time assertion that StaticTokenValidator implements the core
// interfaces.ServiceTokenValidator Strategy seam (another validator can replace it).
var _ interfaces.ServiceTokenValidator = interceptors.NewStaticTokenValidator(nil)

// TestServerBuilder_WithServiceAuth tests that the server builder wires the
// service-to-service auth interceptor into the assembled internal chain.
//
// Why this test is important:
//   - WithServiceAuth is how the internal (non-gateway) mounts opt into caller
//     authentication; if Build dropped it the internal surface would stay
//     unauthenticated. Its placement (after logging, before the end-user auth
//     interceptor) is fixed by Build; the interceptor tests below prove its behaviour.
//
// What it tests:
//   - a bare builder adds no options; adding WithServiceAuth alone makes Build non-empty
func TestServerBuilder_WithServiceAuth(t *testing.T) {
	t.Parallel()
	validator := interceptors.NewStaticTokenValidator(
		map[string]string{"orders": "tok-orders"},
	)
	assert.Empty(t, interceptors.NewServerBuilder().Build(),
		"no interceptors configured → no options")
	assert.NotEmpty(t,
		interceptors.NewServerBuilder().
			WithLogging(fixtures.NopLogger()).
			WithServiceAuth(validator, false, false).
			Build(),
		"WithServiceAuth must add the service-auth interceptor")
}

// bearerReq builds a unary AnyRequest carrying an "Authorization: Bearer <token>" header
// (empty token → no header), for exercising the service-auth interceptor.
func bearerReq(token string) connect.AnyRequest {
	req := connect.NewRequest[*emptyMessage](nil)
	if token != "" {
		req.Header().Set("Authorization", "Bearer "+token)
	}
	return req
}

// TestServiceAuthInterceptor_Unary tests that the interceptor authenticates the calling
// service by bearer token on unary RPCs: a valid token admits the call and puts the
// caller identity on the context; a missing or wrong token is rejected fail-closed.
//
// Why this test is important:
//   - Without server-side validation any network-reachable actor
//     drives the internal RPCs; the interceptor must reject an unauthenticated caller
//     BEFORE the handler runs, and expose WHICH service called for audit/authz
//
// What it tests:
//   - valid token → handler runs, GetCallerIdentity == the caller; missing/wrong token →
//     CodeUnauthenticated and the handler never runs
func TestServiceAuthInterceptor_Unary(t *testing.T) {
	t.Parallel()
	validator := interceptors.NewStaticTokenValidator(
		map[string]string{"orders": "tok-orders"},
	)
	interceptor := interceptors.NewServiceAuthInterceptor(
		validator,
		false,
		false,
		fixtures.NopLogger(),
	)

	t.Run("valid token admits + attaches caller", func(t *testing.T) {
		t.Parallel()
		var gotCaller string
		var gotOK, ran bool
		next := func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			ran = true
			gotCaller, gotOK = interceptors.GetCallerIdentity(ctx)
			return newTestResponse(), nil
		}
		resp, err := interceptor.WrapUnary(
			next,
		)(
			context.Background(),
			bearerReq("tok-orders"),
		)
		require.NoError(t, err)
		assert.NotNil(t, resp)
		assert.True(t, ran, "handler must run for an authenticated caller")
		assert.True(t, gotOK)
		assert.Equal(t, "orders", gotCaller)
	})

	for _, tc := range []struct {
		name  string
		token string
	}{
		{"missing token rejected", ""},
		{"wrong token rejected", "tok-wrong"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ran := false
			next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
				ran = true
				return newTestResponse(), nil
			}
			resp, err := interceptor.WrapUnary(
				next,
			)(
				context.Background(),
				bearerReq(tc.token),
			)
			assert.Nil(t, resp)
			require.Error(t, err)
			assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
			assert.False(t, ran, "handler must NOT run for an unauthenticated caller")
		})
	}
}

// TestServiceAuthInterceptor_StubAndAudit tests the two non-enforcing modes: the dev stub
// bypass and the staged-rollout audit mode both admit a token-less call.
//
// Why this test is important:
// - stub keeps a local dev stack working with no provisioned tokens (dev bypass)
//   - audit mode is the rollout safety valve: log the caller but do not reject, so a
//     missing token doesn't fail-closed before every client has rolled to a token
//
// What it tests:
//   - stub=true + no token → handler runs with a synthetic caller
//   - audit=true + no token → handler runs (admitted), no error
func TestServiceAuthInterceptor_StubAndAudit(t *testing.T) {
	t.Parallel()
	validator := interceptors.NewStaticTokenValidator(
		map[string]string{"orders": "tok-orders"},
	)

	t.Run("stub bypass admits with synthetic caller", func(t *testing.T) {
		t.Parallel()
		var caller string
		var ok, ran bool
		next := func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			ran = true
			caller, ok = interceptors.GetCallerIdentity(ctx)
			return newTestResponse(), nil
		}
		interceptor := interceptors.NewServiceAuthInterceptor(
			validator,
			true,
			false,
			fixtures.NopLogger(),
		)
		_, err := interceptor.WrapUnary(next)(context.Background(), bearerReq(""))
		require.NoError(t, err)
		assert.True(t, ran)
		assert.True(t, ok)
		assert.NotEmpty(t, caller, "stub mode injects a synthetic caller identity")
	})

	t.Run("audit mode admits an invalid caller", func(t *testing.T) {
		t.Parallel()
		ran := false
		next := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
			ran = true
			return newTestResponse(), nil
		}
		interceptor := interceptors.NewServiceAuthInterceptor(
			validator,
			false,
			true,
			fixtures.NopLogger(),
		)
		_, err := interceptor.WrapUnary(next)(context.Background(), bearerReq(""))
		require.NoError(t, err, "audit mode logs but does not reject")
		assert.True(t, ran)
	})
}

// TestServiceAuthInterceptor_Streaming tests that the interceptor also guards
// server-streaming RPCs (Connect applies a unary interceptor only to unary calls, so a
// unary-only guard would leave streams unauthenticated).
//
// Why this test is important:
//   - The internal surface includes streaming RPCs; a missing bearer token must reject a
//     stream before the handler runs, identically to a unary call
//
// What it tests:
//   - no token → WrapStreamingHandler returns CodeUnauthenticated and never invokes next;
//     a valid token admits the stream with the caller on the context
func TestServiceAuthInterceptor_Streaming(t *testing.T) {
	t.Parallel()
	validator := interceptors.NewStaticTokenValidator(
		map[string]string{"orders": "tok-orders"},
	)
	interceptor := interceptors.NewServiceAuthInterceptor(
		validator,
		false,
		false,
		fixtures.NopLogger(),
	)

	newConn := func(token string) *mocks.MockStreamingHandlerConn {
		header := http.Header{}
		if token != "" {
			header.Set("Authorization", "Bearer "+token)
		}
		conn := mocks.NewMockStreamingHandlerConn(gomock.NewController(t))
		conn.EXPECT().RequestHeader().Return(header).AnyTimes()
		conn.EXPECT().
			Spec().
			Return(connect.Spec{Procedure: "/test.v1.Probe/Do"}).
			AnyTimes()
		return conn
	}

	t.Run("missing token rejects the stream", func(t *testing.T) {
		t.Parallel()
		ran := false
		next := func(context.Context, connect.StreamingHandlerConn) error {
			ran = true
			return nil
		}
		err := interceptor.WrapStreamingHandler(next)(context.Background(), newConn(""))
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
		assert.False(t, ran)
	})

	t.Run("valid token admits the stream with caller", func(t *testing.T) {
		t.Parallel()
		var caller string
		next := func(ctx context.Context, _ connect.StreamingHandlerConn) error {
			caller, _ = interceptors.GetCallerIdentity(ctx)
			return nil
		}
		err := interceptor.WrapStreamingHandler(
			next,
		)(
			context.Background(),
			newConn("tok-orders"),
		)
		require.NoError(t, err)
		assert.Equal(t, "orders", caller)
	})
}
