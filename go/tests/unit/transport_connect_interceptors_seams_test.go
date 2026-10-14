package unit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	coreprincipal "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/principal"
	coretenant "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/tenant"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/connect/interceptors"
)

// principal is a consumer-owned principal type, standing in for an application's own
// authorization model.
type principal struct {
	Sub    string
	Tenant string
}

// otherPrincipal is a second consumer principal type, used to show principal types never
// share a context slot.
type otherPrincipal struct {
	ID string
}

// customHeaders returns a gateway header contract that differs from testHeaders.
func customHeaders() interceptors.HeaderMap {
	return interceptors.HeaderMap{
		Sub:      "X-Sub",
		Tenant:   "X-Tenant",
		Email:    "X-Mail",
		Name:     "X-Full-Name",
		NickName: "X-Nick",
		Roles:    "X-Groups",
	}
}

// serverPath invokes an interceptor on one server path (unary or streaming) and
// returns the context the handler saw.
type serverPath func(connect.Interceptor, context.Context) (context.Context, error)

// serverPaths returns both server paths of an interceptor, so each seam test covers
// unary and streaming RPCs alike.
func serverPaths() map[string]serverPath {
	return map[string]serverPath{
		"unary":     invokeUnary,
		"streaming": invokeStreaming,
	}
}

// TestAuthInterceptor_HeaderMap tests that the auth interceptor reads claims from the
// headers a consumer's gateway actually sets.
//
// Why this test is important:
//   - Gateways name identity headers differently; a fixed header contract ties the
//     library to one deployment.
//   - Gateways commonly join roles with ", "; an untrimmed role never matches a role
//     check.
//
// What it tests:
//   - With a custom HeaderMap, every claim is read from the custom headers, the role list
//     is split on commas with surrounding whitespace trimmed, and the claims are not
//     marked synthetic.
//   - Headers outside the map are ignored.
func TestAuthInterceptor_HeaderMap(t *testing.T) {
	t.Parallel()
	ic := interceptors.NewAuthInterceptor(false, customHeaders())

	ctx, err := invokeUnaryWith(
		ic,
		context.Background(),
		newTestRequestWithHeaders(map[string]string{
			"X-Sub": "u1", "X-Tenant": "t1", "X-Mail": "u1@example.com",
			"X-Full-Name": "User One", "X-Nick": "u1", "X-Groups": "a, b ,",
		}),
	)
	require.NoError(t, err)
	claims, ok := interceptors.GetAuthClaims(ctx)
	require.True(t, ok, "claims must be read from the custom headers")
	assert.Equal(t, interceptors.AuthClaims{
		Sub:      "u1",
		TenantID: "t1",
		Email:    "u1@example.com",
		Name:     "User One",
		NickName: "u1",
		Roles:    []string{"a", "b"},
	}, *claims)

	ctx, err = invokeUnaryWith(
		ic,
		context.Background(),
		newTestRequestWithHeaders(map[string]string{
			testHeaders().Sub: "u1",
		}),
	)
	require.NoError(t, err)
	_, ok = interceptors.GetAuthClaims(ctx)
	assert.False(t, ok, "a header outside the map must be ignored")
}

// TestInterceptorWiring_FailsLoudlyOnMisconfiguration tests that the server interceptor
// constructors reject a wiring mistake at construction instead of misbehaving per
// request.
//
// Why this test is important:
//   - An empty subject header treats every caller as unauthenticated (or, in stub mode,
//     as the stub user); a nil resolver, extractor or interceptor panics on the first
//     request; a tenant interceptor with no stampers or an unnamed session variable never
//     scopes anything. Each is silent until production traffic hits it.
//
// What it tests:
//   - NewAuthInterceptor panics on a HeaderMap without Sub.
//   - NewPrincipalInterceptor panics on a nil resolver.
//   - NewTenantScopeInterceptor panics on a nil extractor, no stampers or a nil stamper,
//     and SessionVarStamper panics on an empty variable name.
func TestInterceptorWiring_FailsLoudlyOnMisconfiguration(t *testing.T) {
	t.Parallel()
	extract := func(context.Context) (uuid.UUID, bool) { return uuid.Nil, false }

	assert.Panics(
		t,
		func() { interceptors.NewAuthInterceptor(true, interceptors.HeaderMap{Tenant: "X-T"}) },
		"NewAuthInterceptor needs a subject header",
	)
	assert.Panics(t, func() { interceptors.NewPrincipalInterceptor[principal](nil, nil) },
		"NewPrincipalInterceptor needs a resolver")
	assert.Panics(
		t,
		func() { interceptors.NewTenantScopeInterceptor(nil, interceptors.StampTenantContext) },
		"NewTenantScopeInterceptor needs an extractor",
	)
	assert.Panics(t, func() { interceptors.NewTenantScopeInterceptor(extract) },
		"NewTenantScopeInterceptor needs a stamper")
	assert.Panics(t, func() { interceptors.NewTenantScopeInterceptor(extract, nil) },
		"NewTenantScopeInterceptor rejects a nil stamper")
	assert.Panics(t, func() { interceptors.SessionVarStamper("") },
		"SessionVarStamper needs a variable name")
}

// TestPrincipalInterceptor_ResolvesConsumerPrincipal tests that the principal interceptor
// turns claims into the consumer's own principal type on unary and streaming RPCs.
//
// Why this test is important:
//   - Authorization models are application-specific; the library must carry the
//     consumer's principal, not define one.
//   - A streaming path that dropped the resolved context would run server-streaming
//     handlers with no principal; a handler reading cleared claims as present would
//     dereference nil instead of returning Unauthenticated.
//
// What it tests:
//   - On both paths, claims are resolved into the consumer's principal, readable via
//     core/principal.PrincipalFrom, and GetAuthClaims then reports no claims.
//   - A principal of one type is invisible to PrincipalFrom of another type.
//   - A request without claims passes through with no principal and no resolver call.
func TestPrincipalInterceptor_ResolvesConsumerPrincipal(t *testing.T) {
	t.Parallel()
	for name, invoke := range serverPaths() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			resolve := func(_ context.Context, c *interceptors.AuthClaims) (principal, error) {
				calls++
				return principal{Sub: c.Sub, Tenant: c.TenantID}, nil
			}
			ic := interceptors.NewPrincipalInterceptor[principal](resolve, nil)

			ctx := interceptors.WithAuthClaims(context.Background(),
				&interceptors.AuthClaims{Sub: "u1", TenantID: "t1"})
			got, err := invoke(ic, ctx)
			require.NoError(t, err)
			p, ok := coreprincipal.PrincipalFrom[principal](got)
			require.True(t, ok)
			assert.Equal(t, principal{Sub: "u1", Tenant: "t1"}, p)
			claims, ok := interceptors.GetAuthClaims(got)
			assert.False(t, ok, "cleared claims must read as absent")
			assert.Nil(t, claims, "raw claims must be cleared once resolved")
			_, ok = coreprincipal.PrincipalFrom[otherPrincipal](got)
			assert.False(t, ok, "a principal of another type must not be visible")

			got, err = invoke(ic, context.Background())
			require.NoError(t, err)
			_, ok = coreprincipal.PrincipalFrom[principal](got)
			assert.False(t, ok, "no claims → no principal")
			assert.Equal(t, 1, calls, "no claims → no resolver call")
		})
	}
}

// TestPrincipalInterceptor_StubAndErrors tests the local-dev stub path and how resolver
// failures reach the caller.
//
// Why this test is important:
//   - The stub must apply only to synthetic claims, or a real caller behind the gateway
//     is silently given the stub identity.
//   - A resolver following the error-code convention must reach the caller with the
//     matching Connect code: a rejected caller retried as Unavailable never sees
//     PermissionDenied, and a transient outage reported as permanent logs the caller out.
//   - Resolver errors are consumer code (often a database error); their text must not
//     reach the client.
//
// What it tests:
//   - Synthetic claims get the stub principal without a resolver call; real claims
//     resolve; with no stub, synthetic claims resolve too.
//   - On both paths a resolver error fails closed: a Connect code is kept, a core/errors
//     code maps to its Connect code (RESOURCE_EXHAUSTED stays ResourceExhausted so a
//     quota rejection is not reported as Internal), and an uncoded error becomes
//     Unavailable.
//   - The client-facing message never contains the resolver's error text; the real error
//     is captured in the request's error sink for the access log.
func TestPrincipalInterceptor_StubAndErrors(t *testing.T) {
	t.Parallel()
	stub := func(c *interceptors.AuthClaims) principal {
		return principal{Sub: c.Sub, Tenant: "stub"}
	}
	resolve := func(_ context.Context, c *interceptors.AuthClaims) (principal, error) {
		return principal{Sub: c.Sub, Tenant: "resolved"}, nil
	}
	synthetic := &interceptors.AuthClaims{Sub: "dev", Synthetic: true}

	got, err := invokeUnary(
		interceptors.NewPrincipalInterceptor[principal](resolve, stub),
		interceptors.WithAuthClaims(context.Background(), synthetic),
	)
	require.NoError(t, err)
	p, _ := coreprincipal.PrincipalFrom[principal](got)
	assert.Equal(t, "stub", p.Tenant, "synthetic claims take the stub principal")

	got, err = invokeUnary(
		interceptors.NewPrincipalInterceptor[principal](resolve, stub),
		interceptors.WithAuthClaims(
			context.Background(),
			&interceptors.AuthClaims{Sub: "real"},
		),
	)
	require.NoError(t, err)
	p, _ = coreprincipal.PrincipalFrom[principal](got)
	assert.Equal(t, "resolved", p.Tenant, "real claims resolve even with a stub set")

	got, err = invokeUnary(interceptors.NewPrincipalInterceptor[principal](resolve, nil),
		interceptors.WithAuthClaims(context.Background(), synthetic))
	require.NoError(t, err)
	p, _ = coreprincipal.PrincipalFrom[principal](got)
	assert.Equal(t, "resolved", p.Tenant, "without a stub, synthetic claims resolve")

	const secret = "pq: host db-7.internal password auth failed"
	cases := map[string]struct {
		err  error
		want connect.Code
	}{
		"connect code": {
			connect.NewError(connect.CodeUnauthenticated, apperr.Sentinel(secret)),
			connect.CodeUnauthenticated,
		},
		"forbidden":     {apperr.Forbidden(secret), connect.CodePermissionDenied},
		"unauthorized":  {apperr.Unauthorized(secret), connect.CodeUnauthenticated},
		"not found":     {apperr.NotFound(secret), connect.CodeNotFound},
		"invalid input": {apperr.InvalidInput(secret), connect.CodeInvalidArgument},
		"timeout":       {apperr.Timeout(secret), connect.CodeDeadlineExceeded},
		"unavailable":   {apperr.Unavailable(secret), connect.CodeUnavailable},
		"internal": {
			apperr.Wrap(apperr.Sentinel(secret), apperr.CodeInternal, "lookup"),
			connect.CodeInternal,
		},
		"uncoded": {apperr.Sentinel(secret), connect.CodeUnavailable},
		"wrapped coded": {
			apperr.Wrap(apperr.Forbidden(secret), apperr.CodeForbidden, "resolve"),
			connect.CodePermissionDenied,
		},
		"upstream coded": {apperr.Upstream(secret), connect.CodeUnavailable},
		"resource exhausted": {
			apperr.Wrap(apperr.Sentinel(secret), apperr.CodeResourceExhausted, "quota"),
			connect.CodeResourceExhausted,
		},
	}
	for path, invoke := range serverPaths() {
		for name, tc := range cases {
			failing := func(context.Context, *interceptors.AuthClaims) (principal, error) {
				return principal{}, tc.err
			}
			ctx := apperr.WithSink(interceptors.WithAuthClaims(context.Background(),
				&interceptors.AuthClaims{Sub: "u"}))
			handled, err := invoke(
				interceptors.NewPrincipalInterceptor[principal](failing, nil),
				ctx,
			)
			label := path + "/" + name
			assert.Nil(t, handled, "%s: the handler must not run", label)
			assert.Equal(t, tc.want, connect.CodeOf(err), label)
			var connectErr *connect.Error
			require.ErrorAs(t, err, &connectErr, label)
			assert.NotContains(
				t,
				connectErr.Message(),
				"db-7",
				"%s: resolver text leaked",
				label,
			)
			assert.ErrorIs(
				t,
				apperr.Captured(ctx),
				tc.err,
				"%s: the real cause must be captured",
				label,
			)
		}
	}
}

// TestPrincipalInterceptor_RejectsNilPrincipal tests that a nil principal from the
// resolver or the stub fails the request instead of being stored as present.
//
// Why this test is important:
//   - With a pointer principal type, a resolver returning (nil, nil) for an unknown
//     caller would pass a handler's `if !ok` check and dereference nil (a 500 via
//     recovery) instead of answering Unauthenticated.
//
// What it tests:
//   - On both paths, a nil principal from the resolver (real claims) or from the stub
//     (synthetic claims) fails with CodeUnauthenticated and never reaches the handler.
func TestPrincipalInterceptor_RejectsNilPrincipal(t *testing.T) {
	t.Parallel()
	nilResolve := func(
		context.Context,
		*interceptors.AuthClaims,
	) (*principal, error) {
		var none *principal
		return none, nil
	}
	nilStub := func(*interceptors.AuthClaims) *principal { return nil }
	ic := interceptors.NewPrincipalInterceptor[*principal](nilResolve, nilStub)

	for path, invoke := range serverPaths() {
		for name, claims := range map[string]*interceptors.AuthClaims{
			"resolver": {Sub: "unknown"},
			"stub":     {Sub: "dev", Synthetic: true},
		} {
			handled, err := invoke(
				ic,
				interceptors.WithAuthClaims(context.Background(), claims),
			)
			assert.Equal(
				t,
				connect.CodeUnauthenticated,
				connect.CodeOf(err),
				path+"/"+name,
			)
			assert.Nil(t, handled, "%s/%s: the handler must not run", path, name)
		}
	}
}

// TestTenantScopeInterceptor_StampsExtractedTenant tests that the tenant interceptor
// stamps whatever tenant the consumer's extractor resolves, with the consumer's stampers,
// on unary and streaming RPCs.
//
// Why this test is important:
//   - Where the tenant comes from and how it scopes persistence (which session variable,
//     which ORM scope) are application choices; a tenant stamped wrongly leaks data
//     across tenants.
//   - The nil UUID is not a tenant: stamping it would scope every tenant-less caller to
//     one shared tenant whose rows they could all read.
//   - A server-streaming handler left unscoped would bypass Row-Level Security.
//
// What it tests:
//   - On both paths, a resolved tenant is stamped by each stamper: the core tenant
//     context and a named SQL session variable.
//   - An unresolved tenant and the nil UUID both leave the context unstamped (no tenant
//     context, no session variable).
//   - Mutating the caller's stamper slice after construction does not change the
//     stampers.
func TestTenantScopeInterceptor_StampsExtractedTenant(t *testing.T) {
	t.Parallel()
	for name, invoke := range serverPaths() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tenant, resolved := uuid.New(), true
			extract := func(context.Context) (uuid.UUID, bool) { return tenant, resolved }
			stampers := []interceptors.TenantStamper{
				interceptors.StampTenantContext,
				interceptors.SessionVarStamper("app.tenant"),
			}
			ic := interceptors.NewTenantScopeInterceptor(extract, stampers...)
			stampers[0] = func(ctx context.Context, _ uuid.UUID) context.Context { return ctx }

			got, err := invoke(ic, context.Background())
			require.NoError(t, err)
			id, ok := coretenant.TenantFromContext(got)
			assert.True(t, ok, "the core tenant context must be stamped")
			assert.Equal(t, tenant, id)
			v, ok := entsql.VarFromContext(got, "app.tenant")
			assert.True(t, ok, "the session variable must be stamped")
			assert.Equal(t, tenant.String(), v)

			for label, set := range map[string]func(){
				"unresolved": func() { tenant, resolved = uuid.New(), false },
				"nil uuid":   func() { tenant, resolved = uuid.Nil, true },
			} {
				set()
				unstamped, err := invoke(ic, context.Background())
				require.NoError(t, err)
				_, ok = coretenant.TenantFromContext(unstamped)
				assert.False(t, ok, "%s: no tenant context may be stamped", label)
				_, ok = entsql.VarFromContext(unstamped, "app.tenant")
				assert.False(t, ok, "%s: no session variable may be stamped", label)
			}
		})
	}
}

// TestServerOptions_RunsAuthnInOrderBeforeInner tests where the server chain runs the
// consumer's own interceptors.
//
// Why this test is important:
//   - A consumer's principal and tenant-scope interceptors need the authenticated claims;
//     running before auth would authorize an anonymous request.
//   - They must run in the order given (tenant scope reads the principal the principal
//     interceptor stored) and before validation, as ServerOptions documents.
//
// What it tests:
//   - Through a Connect handler built from the options (served in process), caller
//     interceptors after the auth interceptor in Edge.Authn see the claims parsed from
//     the consumer's header contract, run in the order given, and run before the
//     validation interceptor in Edge.Inner rejects the request.
func TestServerOptions_RunsAuthnInOrderBeforeInner(t *testing.T) {
	t.Parallel()
	var order []string
	var seen *interceptors.AuthClaims
	probe := func(name string) connect.Interceptor {
		return connect.UnaryInterceptorFunc(
			func(next connect.UnaryFunc) connect.UnaryFunc {
				return func(
					ctx context.Context,
					req connect.AnyRequest,
				) (connect.AnyResponse, error) {
					order = append(order, name)
					if seen == nil {
						seen, _ = interceptors.GetAuthClaims(ctx)
					}
					return next(ctx, req)
				}
			},
		)
	}
	opts := interceptors.ServerOptions("probe", decorate.Deps{}, interceptors.Edge{
		Authn: []connect.Interceptor{
			interceptors.NewAuthInterceptor(false, customHeaders()),
			probe("first"),
			probe("second"),
			probe("third"),
		},
		Inner: []connect.Interceptor{interceptors.ValidateInterceptor()},
	})

	status := serveProbe(
		t.Context(),
		opts,
		map[string]string{"X-Sub": "u1", "X-Tenant": "t1"},
	)
	assert.NotEqual(
		t,
		http.StatusOK,
		status,
		"validation must reject the non-proto probe message",
	)
	assert.Equal(t, []string{"first", "second", "third"}, order,
		"caller interceptors must run in the order given, before validation")
	require.NotNil(t, seen, "the caller interceptors must run after auth")
	assert.Equal(t, "u1", seen.Sub)
	assert.Equal(t, "t1", seen.TenantID)
}

// probeMsg is a non-proto message: with probeCodec a Connect handler accepts it, and the
// validation interceptor rejects it (protovalidate validates proto messages only), so an
// interceptor that runs before validation is observable.
type probeMsg struct{}

// probeCodec replaces Connect's "proto" codec so a handler can carry probeMsg; it encodes
// nothing, since the probe calls send no payload.
type probeCodec struct{}

// Name registers probeCodec under the proto codec's name (content type
// application/proto).
func (probeCodec) Name() string { return "proto" }

// Marshal encodes any message as an empty payload.
func (probeCodec) Marshal(any) ([]byte, error) { return nil, nil }

// Unmarshal leaves the message zero-valued.
func (probeCodec) Unmarshal([]byte, any) error { return nil }

// serveProbe serves one unary Connect call to /test.v1.Probe/Do through a handler built
// with opts, in process through an httptest recorder (no socket), and returns the HTTP
// status.
func serveProbe(
	ctx context.Context,
	opts []connect.HandlerOption,
	headers map[string]string,
) int {
	const procedure = "/test.v1.Probe/Do"
	handler := connect.NewUnaryHandler(
		procedure,
		func(context.Context, *connect.Request[probeMsg]) (*connect.Response[probeMsg], error) {
			return connect.NewResponse(&probeMsg{}), nil
		},
		append(opts, connect.WithCodec(probeCodec{}))...)
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, procedure, http.NoBody)
	req.Header.Set("Content-Type", "application/proto")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code
}

// invokeUnary runs ic's unary path on a minimal request against a pass-through handler
// and returns the context the handler observed (nil when it never ran) plus the error.
func invokeUnary(ic connect.Interceptor, ctx context.Context) (context.Context, error) {
	return invokeUnaryWith(ic, ctx, newTestRequest())
}

// invokeUnaryWith runs ic's unary path on req against a pass-through handler and returns
// the context the handler observed (nil when it never ran) plus the error.
func invokeUnaryWith(
	ic connect.Interceptor, ctx context.Context, req connect.AnyRequest,
) (context.Context, error) {
	seen := make(chan context.Context, 1)
	next := func(c context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		seen <- c
		return newTestResponse(), nil
	}
	_, err := ic.WrapUnary(next)(ctx, req)
	return observed(seen), err
}

// observed returns the context a pass-through handler sent on seen, or nil when the
// handler never ran.
func observed(seen <-chan context.Context) context.Context {
	select {
	case c := <-seen:
		return c
	default:
		return nil
	}
}

// invokeStreaming runs ic's streaming-handler path against a pass-through handler and
// returns the context the handler observed (nil when it never ran) plus the error. The
// seam interceptors never touch the conn, so none is passed.
func invokeStreaming(
	ic connect.Interceptor,
	ctx context.Context,
) (context.Context, error) {
	seen := make(chan context.Context, 1)
	err := ic.WrapStreamingHandler(
		func(c context.Context, _ connect.StreamingHandlerConn) error {
			seen <- c
			return nil
		},
	)(
		ctx,
		nil,
	)
	return observed(seen), err
}
