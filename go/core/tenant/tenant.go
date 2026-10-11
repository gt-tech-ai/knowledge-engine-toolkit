// Package tenant carries the request-scoped tenant (organization) identity in a
// context.Context. It is generic multi-tenancy plumbing with no ORM or domain
// dependency: a transport interceptor stamps the resolved tenant once per
// request, and downstream data-access layers read it to scope every operation to
// that tenant and fail closed when it is absent.
package tenant

import (
	"context"

	"github.com/google/uuid"
)

// tenantKey is a typed context key for the request tenant (organization) id.
type tenantKey struct{}

// WithTenant returns a context carrying the tenant (organization) id.
//
// Example:
//
//	ctx := tenant.WithTenant(ctx, orgID)
func WithTenant(ctx context.Context, tenant uuid.UUID) context.Context {
	return context.WithValue(ctx, tenantKey{}, tenant)
}

// TenantFromContext extracts the tenant (organization) id from context.
// The bool is false (and the uuid is uuid.Nil) when the context is nil or no
// tenant is set — the signal data-access layers use to fail closed.
func TenantFromContext(ctx context.Context) (uuid.UUID, bool) {
	if ctx == nil {
		return uuid.Nil, false
	}
	tenant, ok := ctx.Value(tenantKey{}).(uuid.UUID)
	return tenant, ok
}
