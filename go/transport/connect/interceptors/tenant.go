package interceptors

import (
	"context"

	"connectrpc.com/connect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	coretenant "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/tenant"
)

// TenantExtractor returns the request's resolved tenant id, or false when none is
// resolved (a public or unauthenticated request). The nil UUID also counts as no
// tenant.
//
// Read the tenant from the resolved principal (core/principal.PrincipalFrom) and register
// the tenant-scope interceptor after the principal interceptor: the principal interceptor
// clears the raw AuthClaims, so an extractor reading GetAuthClaims after it never sees a
// tenant.
type TenantExtractor func(ctx context.Context) (uuid.UUID, bool)

// TenantStamper returns ctx scoped to tenant for one persistence mechanism — e.g.
// a Postgres session variable for Row-Level Security, or an ORM tenant scope.
type TenantStamper func(ctx context.Context, tenant uuid.UUID) context.Context

// StampTenantContext stamps the tenant with core/tenant.WithTenant, the context
// value data-access layers read to scope operations and fail closed without one.
func StampTenantContext(ctx context.Context, tenant uuid.UUID) context.Context {
	return coretenant.WithTenant(ctx, tenant)
}

// SessionVarStamper returns a stamper that sets the Postgres session variable
// (GUC) name to the tenant id through Ent's sql.WithVar: Ent emits SET before each
// statement, which drives a Row-Level Security policy reading
// current_setting(name, true) (leak-safe on reads: Ent RESETs the pooled conn).
// It panics if name is empty.
func SessionVarStamper(name string) TenantStamper {
	if name == "" {
		panic("interceptors: SessionVarStamper requires a session variable name")
	}
	return func(ctx context.Context, tenant uuid.UUID) context.Context {
		return entsql.WithVar(ctx, name, tenant.String())
	}
}

// tenantScopeInterceptor stamps the extracted tenant onto the request context with
// each stamper, for BOTH unary and streaming RPCs.
type tenantScopeInterceptor struct {
	// extract resolves the request's tenant.
	extract TenantExtractor
	// stamper applies every configured stamper's tenant scope, in order.
	stamper TenantStamper
}

// NewTenantScopeInterceptor returns an interceptor that stamps the tenant resolved
// by extract onto each request with every stamper. It must run after whatever
// resolves the tenant (typically the principal interceptor). A request with no
// resolved tenant (or the nil UUID) passes through unstamped, so tenant-scoped data
// access downstream fails closed while non-tenant access is unaffected; the
// interceptor itself neither rejects nor logs it.
//
// It panics if extract is nil, no stamper is given or a stamper is nil, so a wiring
// mistake fails at startup instead of leaving every request unscoped. The stampers are
// composed into one chain at construction, so the caller may reuse its slice.
func NewTenantScopeInterceptor(
	extract TenantExtractor,
	stampers ...TenantStamper,
) connect.Interceptor {
	if extract == nil {
		panic("interceptors: NewTenantScopeInterceptor requires a non-nil extractor")
	}
	if len(stampers) == 0 {
		panic("interceptors: NewTenantScopeInterceptor requires at least one stamper")
	}
	for _, s := range stampers {
		if s == nil {
			panic("interceptors: NewTenantScopeInterceptor requires non-nil stampers")
		}
	}
	return tenantScopeInterceptor{extract: extract, stamper: composeStampers(stampers)}
}

// composeStampers folds a non-empty stamper list into one stamper that applies them in
// order. The closures capture the stamper values, not the slice, so later writes to
// the caller's slice do not change the chain.
func composeStampers(stampers []TenantStamper) TenantStamper {
	chain := stampers[0]
	for _, next := range stampers[1:] {
		prev := chain
		chain = func(ctx context.Context, tenant uuid.UUID) context.Context {
			return next(prev(ctx, tenant), tenant)
		}
	}
	return chain
}

// WrapUnary stamps the tenant on the unary request context.
func (i tenantScopeInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return next(i.stamp(ctx), req)
	}
}

// WrapStreamingClient is a no-op — this is a server-side propagation interceptor.
func (i tenantScopeInterceptor) WrapStreamingClient(
	next connect.StreamingClientFunc,
) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler stamps the tenant on the streaming request context, so a
// server-streaming handler carries the same tenant scope.
func (i tenantScopeInterceptor) WrapStreamingHandler(
	next connect.StreamingHandlerFunc,
) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return next(i.stamp(ctx), conn)
	}
}

// stamp applies the stamper chain for the extracted tenant, or returns ctx unchanged
// when none is resolved or the tenant is the nil UUID.
func (i tenantScopeInterceptor) stamp(ctx context.Context) context.Context {
	tenant, ok := i.extract(ctx)
	if !ok || tenant == uuid.Nil {
		return ctx
	}
	return i.stamper(ctx, tenant)
}
