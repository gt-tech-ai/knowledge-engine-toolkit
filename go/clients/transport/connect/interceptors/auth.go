// Package interceptors provides Connect RPC interceptors for cross-cutting concerns.
//
// The auth interceptor extracts the identity headers a gateway sets after validating the
// caller's token (the consumer names them in a HeaderMap) and stores them as AuthClaims
// in the request context. Downstream handlers retrieve claims via GetAuthClaims.
package interceptors

import (
	"context"
	"net/http"
	"strings"

	"connectrpc.com/connect"
)

// HeaderMap names the gateway headers the auth interceptor reads claims from, so a
// consumer matches whatever its gateway sets. Sub is required (NewAuthInterceptor and
// ServerBuilder.WithAuth panic without it); any other empty name leaves that claim
// unset.
type HeaderMap struct {
	// Sub carries the authenticated caller's external subject id; its absence means
	// the request is unauthenticated.
	Sub string
	// Tenant carries the tenant (organization) the caller authenticates under.
	Tenant string
	// Email carries the caller's email address.
	Email string
	// Name carries the caller's full name.
	Name string
	// NickName carries the caller's display name.
	NickName string
	// Roles carries the caller's comma-separated role list.
	Roles string
}

// authContextKey is the private context key under which AuthClaims are stored,
// keeping the claims slot collision-free across packages.
type authContextKey struct{}

// AuthClaims holds the authentication claims extracted from the gateway's identity
// headers. The gateway validates the token; this package only reads what it forwards.
type AuthClaims struct {
	// Sub is the authenticated caller's external subject id.
	Sub string

	// Email is the caller's email address.
	Email string

	// Name is the caller's full name (e.g. "Jane Smith").
	Name string

	// NickName is the caller's display name (e.g. "jane.smith").
	NickName string

	// TenantID is the tenant (organization) the caller authenticates under.
	TenantID string

	// Roles is the caller's role list as the gateway forwards it. Whether it is
	// authoritative for authorization is the consumer's decision.
	Roles []string

	// Synthetic marks claims fabricated by the local-dev stub path (no gateway identity
	// present), as opposed to claims read from gateway headers. A principal interceptor
	// applies its stub principal ONLY to synthetic claims, so with stub enabled behind a
	// gateway a real caller still resolves their true identity.
	Synthetic bool
}

// WithAuthClaims stores auth claims in the context.
func WithAuthClaims(
	ctx context.Context,
	claims *AuthClaims,
) context.Context {
	return context.WithValue(ctx, authContextKey{}, claims)
}

// GetAuthClaims retrieves auth claims from the context. It reports false when none
// are stored or they were cleared with a nil *AuthClaims (as the principal interceptor
// does once it has resolved them), so ok=true always means non-nil claims.
func GetAuthClaims(ctx context.Context) (*AuthClaims, bool) {
	claims, ok := ctx.Value(authContextKey{}).(*AuthClaims)
	return claims, ok && claims != nil
}

// newStubDevClaims returns fresh synthetic claims for local dev when no gateway is
// present. A new value per request prevents concurrent handlers from
// sharing the same pointer.
func newStubDevClaims() *AuthClaims {
	return &AuthClaims{
		Sub:       "stub-user",
		Email:     "stub@example.com",
		Name:      "Stub User",
		NickName:  "stub",
		TenantID:  "stub-tenant",
		Roles:     []string{"admin"},
		Synthetic: true,
	}
}

// authInterceptor extracts identity headers into the request context for BOTH
// unary and streaming RPCs. Streaming coverage is required: Connect applies a
// connect.UnaryInterceptorFunc only to unary calls, so a unary-only auth
// interceptor would leave server-streaming handlers unauthenticated.
type authInterceptor struct {
	// headers names the gateway headers claims are read from.
	headers HeaderMap
	// stub injects synthetic dev claims when no gateway is present (local dev only).
	stub bool
}

// NewAuthInterceptor returns an interceptor that extracts the identity headers named
// in headers into context for unary and streaming handlers. Token validation is NOT
// performed here — it is the gateway's job.
//
// When stub is true (local dev only), synthetic dev claims are injected when the Sub
// header is absent, so local development works without a gateway. When stub is false,
// a request without the Sub header is left unauthenticated; protected handlers must
// call GetAuthClaims and return CodeUnauthenticated.
//
// It panics if headers.Sub is empty: no request could ever be authenticated, and in
// stub mode every real caller would silently get the synthetic claims.
func NewAuthInterceptor(stub bool, headers HeaderMap) connect.Interceptor {
	mustHaveSubHeader(headers)
	return authInterceptor{headers: headers, stub: stub}
}

// mustHaveSubHeader panics when headers names no subject header, failing a wiring
// mistake at startup rather than treating every request as unauthenticated.
func mustHaveSubHeader(headers HeaderMap) {
	if headers.Sub == "" {
		panic("interceptors: HeaderMap.Sub must name the gateway's subject header")
	}
}

// WrapUnary extracts identity headers from the unary request.
func (a authInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return next(a.withClaims(ctx, req.Header()), req)
	}
}

// WrapStreamingClient is a no-op — this is a server-side header-extraction interceptor.
func (a authInterceptor) WrapStreamingClient(
	next connect.StreamingClientFunc,
) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler extracts identity headers from the streaming request so
// streaming handlers see the same AuthClaims as unary handlers.
func (a authInterceptor) WrapStreamingHandler(
	next connect.StreamingHandlerFunc,
) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return next(a.withClaims(ctx, conn.RequestHeader()), conn)
	}
}

// withClaims parses the identity headers into AuthClaims and stores them on the
// returned context. With no subject header it either injects stub dev claims (stub
// mode) or leaves the context unauthenticated.
func (a authInterceptor) withClaims(ctx context.Context, h http.Header) context.Context {
	sub := h.Get(a.headers.Sub)

	if sub == "" && a.stub {
		// Dev shortcut: no gateway, synthesize dev claims.
		return WithAuthClaims(ctx, newStubDevClaims())
	}
	if sub == "" {
		return ctx
	}

	claims := &AuthClaims{
		Sub:      sub,
		Email:    h.Get(a.headers.Email),
		Name:     h.Get(a.headers.Name),
		NickName: h.Get(a.headers.NickName),
		TenantID: h.Get(a.headers.Tenant),
	}
	if roles := h.Get(a.headers.Roles); roles != "" {
		claims.Roles = splitRoles(roles)
	}
	return WithAuthClaims(ctx, claims)
}

// splitRoles splits a comma-separated roles string into a slice, trimming the
// whitespace around each role (gateways often join with ", "). Empty segments are
// discarded.
func splitRoles(s string) []string {
	var roles []string
	for role := range strings.SplitSeq(s, ",") {
		if role = strings.TrimSpace(role); role != "" {
			roles = append(roles, role)
		}
	}
	return roles
}
