// Package principal carries the request's resolved principal — the consumer's own caller
// type (its authorization model) — in a context.Context. It is the principal sibling of
// core/tenant: a transport interceptor stores the principal once per request, and every
// lower tier (services, repositories) reads it here without importing the transport.
package principal

import "context"

// principalKey is the private context key for a principal of type P; each P gets its own
// key, so principals of different types never collide.
type principalKey[P any] struct{}

// WithPrincipal returns a context carrying the caller's resolved principal of type P.
func WithPrincipal[P any](ctx context.Context, principal P) context.Context {
	return context.WithValue(ctx, principalKey[P]{}, principal)
}

// PrincipalFrom returns the caller's principal of type P. The bool is false (and P is its
// zero value) when the context is nil or holds no principal of that type.
func PrincipalFrom[P any](ctx context.Context) (P, bool) {
	var zero P
	if ctx == nil {
		return zero, false
	}
	principal, ok := ctx.Value(principalKey[P]{}).(P)
	return principal, ok
}
