package unit_test

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/budget"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/connect/interceptors"
)

// testHeaders returns the gateway header contract the interceptor tests send, passed to
// the auth interceptor explicitly (the library has no default header names).
func testHeaders() interceptors.HeaderMap {
	return interceptors.HeaderMap{
		Sub:      "X-User-Sub",
		Tenant:   "X-Org-ID",
		Email:    "X-User-Email",
		Name:     "X-User-Name",
		NickName: "X-User-Nickname",
		Roles:    "X-Roles",
	}
}

// newTestRequest returns a minimal AnyRequest for testing.
// Uses connect.NewRequest with a nil proto message.
func newTestRequest() connect.AnyRequest {
	return connect.NewRequest[*emptyMessage](nil)
}

// emptyMessage is a minimal type satisfying proto.Message for test requests.
type emptyMessage struct{}

func (f *emptyMessage) ProtoReflect() {}

// newTestResponse returns a minimal AnyResponse for testing.
func newTestResponse() connect.AnyResponse {
	return connect.NewResponse[*emptyMessage](nil)
}

// ---------------------------------------------------------------------------
// Retry interceptor
// ---------------------------------------------------------------------------

// TestBudgetInterceptor_AttachesSharedBudget verifies the server entrypoint
// attaches a per-request retry budget to the context.
//
// Why this test is important:
//   - Without one shared budget, each retrier in a request's chain retries
//     independently, so a deep call tree can multiply into a retry storm.
//
// What it tests:
//   - A handler wrapped by BudgetInterceptor(n) sees budget.Remaining(ctx) == n;
//     n <= 0 attaches no budget (Remaining reports -1).
func TestBudgetInterceptor_AttachesSharedBudget(t *testing.T) {
	t.Parallel()

	var withBudget int32 = -99
	h := interceptors.BudgetInterceptor(7).WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			withBudget = budget.Remaining(ctx)
			return newTestResponse(), nil
		},
	)
	_, err := h(context.Background(), newTestRequest())
	require.NoError(t, err)
	assert.Equal(
		t,
		int32(7),
		withBudget,
		"the request context must carry the attached budget",
	)

	var disabled int32 = -99
	h0 := interceptors.BudgetInterceptor(0).WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			disabled = budget.Remaining(ctx)
			return newTestResponse(), nil
		},
	)
	_, err = h0(context.Background(), newTestRequest())
	require.NoError(t, err)
	assert.Equal(
		t,
		int32(-1),
		disabled,
		"maxRetries<=0 attaches no budget (Remaining reports -1)",
	)
}

// ---------------------------------------------------------------------------
// Auth interceptor — WithAuthClaims / GetAuthClaims / splitRoles
// ---------------------------------------------------------------------------

// TestWithAuthClaims_GetAuthClaims_RoundTrip tests that auth claims can be
// stored in and retrieved from a context without data loss.
//
// Why this test is important:
//   - Identity propagation via context is the foundation of multi-tenant authorization
//   - Data corruption in any claim field could grant unauthorized access or deny valid
//     users
//   - Every handler depends on this contract to enforce tenant isolation
//
// What it tests:
//   - Claims with Sub, TenantID, and Roles survive a context round-trip
//   - All fields are preserved exactly as set
func TestWithAuthClaims_GetAuthClaims_RoundTrip(t *testing.T) {
	t.Parallel()

	claims := &interceptors.AuthClaims{
		Sub:      "user-1",
		TenantID: "org-1",
		Roles:    []string{"admin", "editor"},
	}
	ctx := interceptors.WithAuthClaims(context.Background(), claims)

	got, ok := interceptors.GetAuthClaims(ctx)
	require.True(t, ok, "expected claims in context")
	assert.Equal(t, "user-1", got.Sub)
	assert.Equal(t, "org-1", got.TenantID)
	assert.Len(t, got.Roles, 2)
}

// TestGetAuthClaims_Missing tests that GetAuthClaims returns false when no
// claims exist in the context.
//
// Why this test is important:
//   - Treating zero-value claims as valid identity would bypass authorization checks
//   - Handlers must be able to distinguish "no auth" from "authenticated with empty
//     fields"
//
// What it tests:
//   - An empty context returns ok=false from GetAuthClaims
func TestGetAuthClaims_Missing(t *testing.T) {
	t.Parallel()

	_, ok := interceptors.GetAuthClaims(context.Background())
	assert.False(t, ok, "expected no claims in empty context")
}

// newTestRequestWithHeaders creates a request with specific HTTP headers.
func newTestRequestWithHeaders(headers map[string]string) connect.AnyRequest {
	req := connect.NewRequest[*emptyMessage](nil)
	for k, v := range headers {
		req.Header().Set(k, v)
	}
	return req
}

// TestAuthInterceptor_WithAllHeaders tests that the auth interceptor extracts
// all identity headers and stores them as AuthClaims in context.
//
// Why this test is important:
//   - The auth interceptor is the entry point for identity propagation in Connect
//     services
//   - Missing or misparse of any header field breaks downstream authorization decisions
//   - Role splitting must handle the comma-separated format used by the API gateway
//
// What it tests:
//   - All four header fields are parsed and stored as AuthClaims
//   - Comma-separated X-Roles header is split into a 3-element slice
func TestAuthInterceptor_WithAllHeaders(t *testing.T) {
	t.Parallel()

	interceptor := interceptors.NewAuthInterceptor(false, testHeaders())
	var (
		claims *interceptors.AuthClaims
		ok     bool
	)

	handler := interceptor.WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			claims, ok = interceptors.GetAuthClaims(ctx)
			return newTestResponse(), nil
		},
	)

	req := newTestRequestWithHeaders(map[string]string{
		testHeaders().Sub:    "user-42",
		testHeaders().Tenant: "org-99",
		testHeaders().Roles:  "admin,editor,viewer",
	})

	resp, err := handler(context.Background(), req)
	require.NoError(t, err, "unexpected error")
	require.NotNil(t, resp, "expected non-nil response")

	require.True(t, ok, "expected claims in context")
	assert.Equal(t, "user-42", claims.Sub)
	assert.Equal(t, "org-99", claims.TenantID)
	assert.Len(t, claims.Roles, 3)
}

// TestAuthInterceptor_NoSub tests that the auth interceptor skips storing
// claims when no X-User-Sub header is present.
//
// Why this test is important:
//   - Storing empty claims would cause handlers to treat unauthenticated requests as
//     authenticated
//   - The interceptor must allow unauthenticated requests through for public endpoints
//   - Downstream auth-required checks depend on claims absence to enforce access control
//
// What it tests:
//   - A request with no headers produces no AuthClaims in context
//   - The handler still executes successfully (no error)
func TestAuthInterceptor_NoSub(t *testing.T) {
	t.Parallel()

	interceptor := interceptors.NewAuthInterceptor(false, testHeaders())
	var ok bool

	handler := interceptor.WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			_, ok = interceptors.GetAuthClaims(ctx)
			return newTestResponse(), nil
		},
	)

	// No headers → no claims stored
	_, err := handler(context.Background(), newTestRequest())
	require.NoError(t, err, "unexpected error")

	assert.False(t, ok, "expected no claims when X-User-Sub is absent")
}

// TestAuthInterceptor_SubOnly_NoRoles tests that the auth interceptor
// stores claims with nil Roles when no X-Roles header is present.
//
// Why this test is important:
//   - nil vs empty-slice semantics affect role-checking logic downstream
//   - Handlers use nil Roles to distinguish "no roles assigned" from "roles checked but
//     empty"
//
// What it tests:
//   - Claims are stored with Sub populated and Roles nil
func TestAuthInterceptor_SubOnly_NoRoles(t *testing.T) {
	t.Parallel()

	interceptor := interceptors.NewAuthInterceptor(false, testHeaders())
	var (
		claims *interceptors.AuthClaims
		ok     bool
	)

	handler := interceptor.WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			claims, ok = interceptors.GetAuthClaims(ctx)
			return newTestResponse(), nil
		},
	)

	req := newTestRequestWithHeaders(map[string]string{
		testHeaders().Sub: "user-1",
	})

	_, err := handler(context.Background(), req)
	require.NoError(t, err, "unexpected error")

	require.True(t, ok, "expected claims in context")
	assert.Equal(t, "user-1", claims.Sub)
	assert.Nil(t, claims.Roles)
}

// TestAuthInterceptor_EmptyRoles tests that an empty X-Roles header results
// in nil Roles rather than a slice with an empty string.
//
// Why this test is important:
//   - A single-element slice containing "" would match empty-string role checks
//     spuriously
//   - Edge case handling of empty headers prevents authorization bypass
//
// What it tests:
//   - An empty X-Roles header value results in Roles being nil
func TestAuthInterceptor_EmptyRoles(t *testing.T) {
	t.Parallel()

	interceptor := interceptors.NewAuthInterceptor(false, testHeaders())
	var (
		claims *interceptors.AuthClaims
		ok     bool
	)

	handler := interceptor.WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			claims, ok = interceptors.GetAuthClaims(ctx)
			return newTestResponse(), nil
		},
	)

	req := newTestRequestWithHeaders(map[string]string{
		testHeaders().Sub:   "user-1",
		testHeaders().Roles: "",
	})

	_, _ = handler(context.Background(), req)

	require.True(t, ok, "expected claims")
	// Empty roles string should NOT set roles
	assert.Nil(t, claims.Roles)
}

// TestAuthInterceptor_RolesWithTrailingComma tests that splitRoles discards
// empty segments from malformed role strings.
//
// Why this test is important:
//   - API gateways may produce trailing or double commas in role headers
//   - Phantom empty-string roles would match unintended authorization rules
//   - Robust parsing prevents security issues from malformed input
//
// What it tests:
//   - The header "admin,,editor," is parsed into exactly 2 roles: admin and editor
func TestAuthInterceptor_RolesWithTrailingComma(t *testing.T) {
	t.Parallel()

	interceptor := interceptors.NewAuthInterceptor(false, testHeaders())
	var (
		claims *interceptors.AuthClaims
		ok     bool
	)

	handler := interceptor.WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			claims, ok = interceptors.GetAuthClaims(ctx)
			return newTestResponse(), nil
		},
	)

	req := newTestRequestWithHeaders(map[string]string{
		testHeaders().Sub:   "user-1",
		testHeaders().Roles: "admin,,editor,",
	})

	_, _ = handler(context.Background(), req)

	require.True(t, ok, "expected claims")
	// splitRoles should discard empty segments
	assert.Len(t, claims.Roles, 2)
}

// TestAuthInterceptor_StubMode_SynthesizesDevClaims tests that stub mode
// synthesizes dev claims, marked synthetic, when no identity headers are present.
//
// Why this test is important:
//   - Local dev has no gateway, so identity headers are never set
//   - Stub mode must provide claims so handlers can proceed in dev
//   - The Synthetic mark is what tells a principal interceptor to apply its stub
//     principal; unmarked stub claims would be resolved as a real "stub-user" caller
//
// What it tests:
//   - With stub=true and no headers, Sub/Email/Name/NickName/TenantID are non-empty
//   - Claims are stored in context (ok=true) and marked Synthetic
func TestAuthInterceptor_StubMode_SynthesizesDevClaims(t *testing.T) {
	t.Parallel()

	interceptor := interceptors.NewAuthInterceptor(true, testHeaders())
	var (
		claims *interceptors.AuthClaims
		ok     bool
	)

	handler := interceptor.WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			claims, ok = interceptors.GetAuthClaims(ctx)
			return newTestResponse(), nil
		},
	)

	_, err := handler(context.Background(), newTestRequest())
	require.NoError(t, err)

	require.True(
		t,
		ok,
		"expected synthesized claims in context when stub=true and no headers",
	)

	assert.NotEmpty(t, claims.Sub, "expected non-empty Sub in stub claims")
	assert.NotEmpty(t, claims.Email, "expected non-empty Email in stub claims")
	assert.NotEmpty(t, claims.Name, "expected non-empty Name in stub claims")
	assert.NotEmpty(t, claims.NickName, "expected non-empty NickName in stub claims")
	assert.NotEmpty(t, claims.TenantID, "expected non-empty TenantID in stub claims")
	assert.True(t, claims.Synthetic, "stub claims must be marked Synthetic")
}

// TestAuthInterceptor_StubMode_RealHeadersWin tests that when stub=true but
// real identity headers ARE present (e.g. running behind a gateway in dev), those
// headers take precedence over the synthesized dev claims.
//
// Why this test is important:
//   - Stub mode must only fill the gap when no gateway is present; if it
//     clobbered real gateway-injected headers, a dev request authenticated as a
//     specific user would silently run as the stub user instead, masking
//     authorization bugs
//   - Header-derived claims marked Synthetic would hand every real caller the
//     consumer's stub principal (a tenant and role bypass)
//
// What it tests:
//   - Real subject / tenant headers are preserved as the claim Sub/TenantID
//     rather than being overwritten by synthesized stub values
//   - The header-derived claims are not marked Synthetic
func TestAuthInterceptor_StubMode_RealHeadersWin(t *testing.T) {
	t.Parallel()

	interceptor := interceptors.NewAuthInterceptor(true, testHeaders())
	var (
		claims *interceptors.AuthClaims
		ok     bool
	)

	handler := interceptor.WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			claims, ok = interceptors.GetAuthClaims(ctx)
			return newTestResponse(), nil
		},
	)

	req := newTestRequestWithHeaders(map[string]string{
		testHeaders().Sub:    "real-user-from-gateway",
		testHeaders().Tenant: "real-org-from-gateway",
	})

	_, err := handler(context.Background(), req)
	require.NoError(t, err, "unexpected error")

	require.True(t, ok, "expected claims in context")
	assert.Equal(t, "real-user-from-gateway", claims.Sub)
	assert.Equal(t, "real-org-from-gateway", claims.TenantID)
	assert.False(
		t,
		claims.Synthetic,
		"header-derived claims must not be marked Synthetic",
	)
}

// ---------------------------------------------------------------------------
// Logging interceptor
// ---------------------------------------------------------------------------

// TestLoggingInterceptor_Success tests that the logging interceptor passes
// through successful responses without modification.
//
// Why this test is important:
//   - Logging must be a transparent decorator that never alters responses
//   - Response mutation by the logging layer would silently corrupt data
//
// What it tests:
//   - A successful handler response passes through unchanged with no error
func TestLoggingInterceptor_Success(t *testing.T) {
	t.Parallel()

	logger := fixtures.NopLogger()
	interceptor := interceptors.NewLoggingInterceptor(logger)

	expected := newTestResponse()
	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return expected, nil
		},
	)

	resp, err := handler(context.Background(), newTestRequest())
	require.NoError(t, err, "unexpected error")
	assert.Equal(t, expected, resp, "expected response to pass through")
}

// TestLoggingInterceptor_Error tests that the logging interceptor propagates
// handler errors unchanged.
//
// Why this test is important:
//   - Error codes carry semantic meaning used by clients for retry and fallback decisions
//   - The logging layer must never re-wrap or swallow errors
//
// What it tests:
//   - A handler returning CodeNotFound propagates as CodeNotFound through the logging
//     interceptor
func TestLoggingInterceptor_Error(t *testing.T) {
	t.Parallel()

	logger := fixtures.NopLogger()
	interceptor := interceptors.NewLoggingInterceptor(logger)

	innerErr := connect.NewError(connect.CodeNotFound, apperr.Sentinel("not found"))
	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return nil, innerErr
		},
	)

	_, err := handler(context.Background(), newTestRequest())
	require.Error(t, err, "expected error to pass through")
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestLoggingInterceptor_UsesWithContext tests that the logging interceptor
// calls logger.WithContext(ctx) so log entries include trace_id and span_id.
//
// Why this test is important:
//   - The tracing interceptor enriches the context with an OTel span before the
//     logging interceptor runs. If the logger doesn't call WithContext, trace
//     correlation fields (trace_id, span_id) are missing from RPC log entries.
//   - Without this, log-trace linking in Grafana (Loki → Tempo) doesn't work.
//
// What it tests:
//   - On success, logger.WithContext(ctx) is called and Info is logged on the
//     context-enriched logger (not the bare logger).
func TestLoggingInterceptor_UsesWithContext_Success(t *testing.T) {
	t.Parallel()

	spy := fixtures.NewSpyLogger()
	interceptor := interceptors.NewLoggingInterceptor(spy)

	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return newTestResponse(), nil
		},
	)

	_, err := handler(context.Background(), newTestRequest())
	require.NoError(t, err, "unexpected error")

	// The bare logger should have zero Info calls — all logging should go
	// through the child returned by WithContext.
	assert.Empty(t, spy.InfoCalls, "expected 0 calls")

	// The child logger (created by WithContext) should have received the Info call.
	assert.Len(t, *spy.ChildInfoCalls, 1)
}

// TestLoggingInterceptor_UsesWithContext_Error tests that the logging interceptor
// calls logger.WithContext(ctx) for error log entries as well.
//
// Why this test is important:
//   - Error logs must also include trace_id/span_id for correlation.
//   - A common mistake is to only enrich the success path but not the error path.
//
// What it tests:
//   - On error, logger.WithContext(ctx) is called and Error is logged on the
//     context-enriched logger (not the bare logger).
func TestLoggingInterceptor_UsesWithContext_Error(t *testing.T) {
	t.Parallel()

	spy := fixtures.NewSpyLogger()
	interceptor := interceptors.NewLoggingInterceptor(spy)

	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return nil, connect.NewError(connect.CodeInternal, apperr.Sentinel("boom"))
		},
	)

	_, _ = handler(context.Background(), newTestRequest())

	// The bare logger should have zero Error calls — all logging should go
	// through the child returned by WithContext.
	assert.Empty(t, spy.ErrorCalls, "expected 0 calls")

	// The child logger (created by WithContext) should have received the Error call.
	assert.Len(t, *spy.ChildErrorCalls, 1)
}

// TestLoggingInterceptor_4xxCodeLogsAtWarn verifies that Connect errors with
// client-error codes (4xx semantics) are logged at Warn, not Error.
//
// Why this test is important:
//   - Log-based alerting commonly fires on any level=error entry.
//   - 4xx errors are normal client conditions (not-found, invalid-argument, etc.)
//     and must NOT trigger a server-error alert.
//   - Only 5xx-equivalent codes (CodeInternal, CodeUnavailable) warrant Error level.
//
// What it tests:
//   - CodeInvalidArgument -> Warn on child logger, zero Error calls on child logger.
func TestLoggingInterceptor_4xxCodeLogsAtWarn(t *testing.T) {
	t.Parallel()

	spy := fixtures.NewSpyLogger()
	interceptor := interceptors.NewLoggingInterceptor(spy)

	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return nil, connect.NewError(
				connect.CodeInvalidArgument,
				apperr.Sentinel("bad input"),
			)
		},
	)

	_, _ = handler(context.Background(), newTestRequest())

	assert.Len(t, *spy.ChildWarnCalls, 1)
	assert.Empty(t, *spy.ChildErrorCalls, "expected 0 calls")
}

// TestLoggingInterceptor_5xxCodeLogsAtError verifies that Connect errors with
// server-error codes (5xx semantics) are logged at Error, not Warn.
//
// Why this test is important:
//   - CodeInternal represents unexpected server failures that operators must investigate.
//   - These must reach Error level (and any server-error alert), unlike 4xx client
//     errors.
//
// What it tests:
//   - CodeInternal -> Error on child logger, zero Warn calls on child logger.
func TestLoggingInterceptor_5xxCodeLogsAtError(t *testing.T) {
	t.Parallel()

	spy := fixtures.NewSpyLogger()
	interceptor := interceptors.NewLoggingInterceptor(spy)

	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return nil, connect.NewError(
				connect.CodeInternal,
				apperr.Sentinel("server fault"),
			)
		},
	)

	_, _ = handler(context.Background(), newTestRequest())

	assert.Len(t, *spy.ChildErrorCalls, 1)
	assert.Empty(t, *spy.ChildWarnCalls, "expected 0 calls")
}

// TestLoggingInterceptor_CanceledLogsAtWarn verifies that a client cancellation
// (CodeCanceled, HTTP 499 client-closed-request) is logged at Warn, not Error.
//
// Why this test is important:
//   - A caller that disconnects or cancels mid-request is a normal client outcome,
//     not a server fault. Logging it at Error trips server-error alerts and inflates
//     the error rate (cancelled reads would surface as error-level 500s).
//
// What it tests:
//   - CodeCanceled -> Warn on the child logger, zero Error calls.
func TestLoggingInterceptor_CanceledLogsAtWarn(t *testing.T) {
	t.Parallel()

	spy := fixtures.NewSpyLogger()
	interceptor := interceptors.NewLoggingInterceptor(spy)

	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return nil, connect.NewError(
				connect.CodeCanceled,
				apperr.Sentinel("client canceled"),
			)
		},
	)

	_, _ = handler(context.Background(), newTestRequest())

	assert.Len(t, *spy.ChildWarnCalls, 1)
	assert.Empty(t, *spy.ChildErrorCalls, "a cancellation must not log at Error")
}

// ---------------------------------------------------------------------------
// Metrics interceptor
// ---------------------------------------------------------------------------

// TestMetricsInterceptor_Success tests that the metrics interceptor passes
// through successful responses without modification.
//
// Why this test is important:
//   - Metrics collection must be transparent and never alter the response
//   - A broken metrics interceptor could silently corrupt responses for all handlers
//
// What it tests:
//   - A successful handler response passes through unchanged with no error
func TestMetricsInterceptor_Success(t *testing.T) {
	t.Parallel()

	metrics := fixtures.NopMetrics()
	interceptor := interceptors.MetricsInterceptor(metrics)

	expected := newTestResponse()
	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return expected, nil
		},
	)

	resp, err := handler(context.Background(), newTestRequest())
	require.NoError(t, err, "unexpected error")
	assert.Equal(t, expected, resp, "expected response to pass through")
}

// TestMetricsInterceptor_Error tests that the metrics interceptor propagates
// handler errors unchanged while recording failure metrics.
//
// Why this test is important:
//   - Error codes must pass through unmodified so clients can react appropriately
//   - Metrics recording must not interfere with error propagation
//
// What it tests:
//   - A handler returning CodeInternal propagates as an error through the metrics
//     interceptor
func TestMetricsInterceptor_Error(t *testing.T) {
	t.Parallel()

	metrics := fixtures.NopMetrics()
	interceptor := interceptors.MetricsInterceptor(metrics)

	innerErr := connect.NewError(connect.CodeInternal, apperr.Sentinel("boom"))
	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return nil, innerErr
		},
	)

	_, err := handler(context.Background(), newTestRequest())
	require.Error(t, err, "expected error to pass through")
}

// TestMetricsInterceptor_WithProcedure tests that the metrics interceptor
// handles requests with an empty Spec().Procedure without panicking.
//
// Why this test is important:
//   - Edge cases in procedure parsing can cause panics that crash the server
//   - Empty procedure strings occur with malformed or internal requests
//   - The metrics layer must be resilient to unexpected input shapes
//
// What it tests:
//   - A request with an empty procedure string completes without error or panic
func TestMetricsInterceptor_WithProcedure(t *testing.T) {
	t.Parallel()

	metrics := fixtures.NopMetrics()
	interceptor := interceptors.MetricsInterceptor(metrics)

	// The newTestRequest has an empty Spec().Procedure, which exercises
	// parseConnectProcedure with a minimal input. This test confirms that the
	// interceptor handles it without panicking.
	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return newTestResponse(), nil
		},
	)

	_, err := handler(context.Background(), newTestRequest())
	require.NoError(t, err, "unexpected error")
}

// ---------------------------------------------------------------------------
// Tracing interceptor
// ---------------------------------------------------------------------------

// TestTracingInterceptor_Success tests that the tracing interceptor passes
// through successful responses without modification.
//
// Why this test is important:
//   - Tracing must be a transparent decorator that never alters responses
//   - A broken tracing interceptor could silently corrupt data across all handlers
//
// What it tests:
//   - A successful handler response passes through unchanged with no error
func TestTracingInterceptor_Success(t *testing.T) {
	t.Parallel()

	tracer := fixtures.NopTracer()
	interceptor := interceptors.TracingInterceptor(tracer)

	expected := newTestResponse()
	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return expected, nil
		},
	)

	resp, err := handler(context.Background(), newTestRequest())
	require.NoError(t, err, "unexpected error")
	assert.Equal(t, expected, resp, "expected response to pass through")
}

// TestTracingInterceptor_Error tests that the tracing interceptor propagates
// handler errors unchanged while recording the error on the trace span.
//
// Why this test is important:
//   - Error codes must pass through unmodified for correct client-side handling
//   - Tracing must record errors for observability without altering the error chain
//
// What it tests:
//   - A handler returning CodeInternal propagates as CodeInternal through the tracing
//     interceptor
func TestTracingInterceptor_Error(t *testing.T) {
	t.Parallel()

	tracer := fixtures.NopTracer()
	interceptor := interceptors.TracingInterceptor(tracer)

	innerErr := connect.NewError(connect.CodeInternal, apperr.Sentinel("boom"))
	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return nil, innerErr
		},
	)

	_, err := handler(context.Background(), newTestRequest())
	require.Error(t, err, "expected error to pass through")
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
}

// TestTracingInterceptor_ExtractsTraceContext tests that the server-side
// tracing interceptor extracts W3C Trace Context from incoming headers.
//
// Why this test is important:
//   - Without trace context extraction, each service starts a new root trace
//   - Distributed tracing requires spans to be parented correctly across service
//     boundaries
//   - W3C Trace Context is the standard propagation format for HTTP-based RPC
//
// What it tests:
//   - A request with traceparent header passes through the interceptor without error
//   - The interceptor calls otel.GetTextMapPropagator().Extract() (verified by no panic)
func TestTracingInterceptor_ExtractsTraceContext(t *testing.T) {
	// NOT parallel: this test mutates the global OTel propagator via
	// otel.SetTextMapPropagator.
	prev := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(prev) })

	// Create a test span context with a known trace ID
	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	spanCtx := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})

	// Inject the trace context into HTTP headers using a real propagator
	headers := make(http.Header)
	propagation.TraceContext{}.Inject(
		trace.ContextWithSpanContext(context.Background(), spanCtx),
		propagation.HeaderCarrier(headers),
	)

	// Create a Connect request with the headers
	req := connect.NewRequest[*emptyMessage](nil)
	for k, v := range headers {
		req.Header().Set(k, v[0])
	}

	require.NotEmpty(
		t,
		req.Header().Get("traceparent"),
		"expected traceparent header in request",
	)

	tracer := fixtures.NopTracer()
	interceptor := interceptors.TracingInterceptor(tracer)

	// Capture the context the inner handler receives to verify extraction.
	var extracted trace.SpanContext
	handler := interceptor.WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			extracted = trace.SpanContextFromContext(ctx)
			return newTestResponse(), nil
		},
	)

	_, err := handler(context.Background(), req)
	require.NoError(t, err, "unexpected error")

	// Verify the trace context was extracted from headers into the Go context.
	require.True(
		t,
		extracted.IsValid(),
		"expected valid span context after extraction, got invalid",
	)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", extracted.TraceID().String())
}

// TestTracingInterceptor_RecordsRequestID tests that the tracing interceptor
// records the X-Request-ID header as a span attribute.
//
// Why this test is important:
//   - X-Request-ID from the gateway is used for correlation with gateway logs
//   - Recording it as a span attribute enables cross-referencing between gateway and
//     service traces
//   - Bridges two separate correlation systems (the gateway's request ID and OTel's trace
//     ID)
//
// What it tests:
//   - A request with X-Request-ID header results in http.request_id span attribute
func TestTracingInterceptor_RecordsRequestID(t *testing.T) {
	t.Parallel()

	req := connect.NewRequest[*emptyMessage](nil)
	req.Header().Set("X-Request-ID", "gw-abc123")

	// Use a spy tracer to capture span attributes
	var recordedAttrs map[string]any
	spyTracer := fixtures.SpyTracer(func(key string, value any) {
		if recordedAttrs == nil {
			recordedAttrs = make(map[string]any)
		}
		recordedAttrs[key] = value
	})

	interceptor := interceptors.TracingInterceptor(spyTracer)
	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return newTestResponse(), nil
		},
	)

	_, err := handler(context.Background(), req)
	require.NoError(t, err, "unexpected error")

	requestID, ok := recordedAttrs["http.request_id"]
	assert.True(t, ok, "expected http.request_id span attribute")
	assert.Equal(t, "gw-abc123", requestID)
}

// ---------------------------------------------------------------------------
// Auth interceptor — GetAuthClaims/WithAuthClaims context roundtrip
// ---------------------------------------------------------------------------

// TestGetAuthClaims_Roundtrip tests the complete WithAuthClaims → GetAuthClaims
// context roundtrip with all fields populated.
//
// Why this test is important:
//   - Every authenticated endpoint depends on GetAuthClaims returning the
//     claims stored by the auth interceptor; field truncation or type mismatches
//     would silently break authorization
//
// What it tests:
//   - All AuthClaims fields survive the context roundtrip
func TestGetAuthClaims_Roundtrip(t *testing.T) {
	t.Parallel()

	original := &interceptors.AuthClaims{
		Sub:      "user-abc-123",
		TenantID: "org-xyz-789",
		Roles:    []string{"admin", "editor"},
	}

	ctx := interceptors.WithAuthClaims(context.Background(), original)
	claims, ok := interceptors.GetAuthClaims(ctx)
	require.True(t, ok, "expected claims in context")
	assert.Equal(t, original.Sub, claims.Sub)
	assert.Equal(t, original.TenantID, claims.TenantID)
	assert.Equal(t, []string{"admin", "editor"}, claims.Roles)
}

// TestGetAuthClaims_MissingReturnsNil tests that GetAuthClaims returns false
// on a context without claims.
//
// Why this test is important:
//   - Unauthenticated requests must return (zero, false); returning true with
//     empty claims would bypass authorization checks
//
// What it tests:
//   - GetAuthClaims on empty context returns (zero, false)
func TestGetAuthClaims_MissingReturnsNil(t *testing.T) {
	t.Parallel()

	claims, ok := interceptors.GetAuthClaims(context.Background())
	assert.False(t, ok)
	assert.Nil(t, claims)
}

// ---------------------------------------------------------------------------
// Auth interceptor — splitRoles edge cases
// ---------------------------------------------------------------------------

// TestAuthInterceptor_SplitRoles_DoubleComma tests that double commas in roles
// header are handled correctly (empty segments discarded).
//
// Why this test is important:
//   - Upstream systems may produce malformed role strings; the parser must
//     handle them gracefully without creating empty-string roles
//
// What it tests:
//   - "admin,,editor" produces ["admin", "editor"], not ["admin", "", "editor"]
func TestAuthInterceptor_SplitRoles_DoubleComma(t *testing.T) {
	t.Parallel()

	interceptor := interceptors.NewAuthInterceptor(false, testHeaders())
	handler := interceptor.WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			claims, ok := interceptors.GetAuthClaims(ctx)
			assert.True(t, ok, "expected claims in context")
			if !ok {
				return newTestResponse(), nil
			}
			assert.Len(t, claims.Roles, 2)
			return newTestResponse(), nil
		},
	)

	req := newTestRequest()
	req.Header().Set(testHeaders().Sub, "user-1")
	req.Header().Set(testHeaders().Roles, "admin,,editor")
	_, _ = handler(context.Background(), req)
}

// TestAuthInterceptor_SingleRole tests that an X-Roles header with one role and
// no commas parses into a single-element Roles slice.
//
// Why this test is important:
//   - The common case (exactly one role) must not be mangled by the comma-split
//     logic that exists to handle multi-role headers; a regression here would
//     break role-based authorization for the majority of users
//
// What it tests:
//   - X-Roles "viewer" produces Roles == ["viewer"]
func TestAuthInterceptor_SingleRole(t *testing.T) {
	t.Parallel()

	interceptor := interceptors.NewAuthInterceptor(false, testHeaders())
	handler := interceptor.WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			claims, ok := interceptors.GetAuthClaims(ctx)
			assert.True(t, ok, "expected claims")
			if !ok {
				return newTestResponse(), nil
			}
			assert.Equal(t, []string{"viewer"}, claims.Roles)
			return newTestResponse(), nil
		},
	)

	req := newTestRequest()
	req.Header().Set(testHeaders().Sub, "user-1")
	req.Header().Set(testHeaders().Roles, "viewer")
	_, _ = handler(context.Background(), req)
}

// ---------------------------------------------------------------------------
// Client builder — partial compositions
// ---------------------------------------------------------------------------

// TestAuthInterceptor_StreamingExtractsHeaders tests that the auth interceptor
// extracts identity headers on the STREAMING path.
//
// Why this test is important:
//   - Connect applies a unary interceptor only to unary calls; a unary-only auth
//     interceptor would leave server-streaming handlers unauthenticated
//
// What it tests:
//   - WrapStreamingHandler parses the request headers into AuthClaims the streaming
//     handler observes
func TestAuthInterceptor_StreamingExtractsHeaders(t *testing.T) {
	t.Parallel()

	interceptor := interceptors.NewAuthInterceptor(false, testHeaders())
	var (
		claims *interceptors.AuthClaims
		ok     bool
	)
	next := func(ctx context.Context, _ connect.StreamingHandlerConn) error {
		claims, ok = interceptors.GetAuthClaims(ctx)
		return nil
	}

	// The streaming auth path reads only RequestHeader (it enriches the context and
	// hands the conn straight to next), so a generated StreamingHandlerConn mock that
	// returns the request headers is all this test needs.
	header := http.Header{}
	header.Set(testHeaders().Sub, "stream-user")
	header.Set(testHeaders().Tenant, "org-7")
	conn := mocks.NewMockStreamingHandlerConn(gomock.NewController(t))
	conn.EXPECT().RequestHeader().Return(header).AnyTimes()

	err := interceptor.WrapStreamingHandler(next)(context.Background(), conn)
	require.NoError(t, err)

	require.True(t, ok, "streaming handler should see auth claims")
	assert.Equal(t, "stream-user", claims.Sub)
	assert.Equal(t, "org-7", claims.TenantID)
}
