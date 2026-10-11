// Package decorate is the type-agnostic operation-decoration mechanism:
// a common OpMiddleware interface, a Chain composer, and a generic Exec[R] runner
// that wraps ANY named operation with the cross-cutting decorator stack, regardless
// of the operation's return type.
//
// It replaces the entity-typed custom-operation seam (the former
// DecoratedRepository[T,P,ID].ExecWithDecoration, hard-typed to (*T,error)) so a
// custom op returning bool / a slice / struct{} decorates without the (*T,error)
// shoehorn, and the cross-cutting bodies (tracing, metrics, logging, timeout, retry,
// circuit-breaker, recovery, auth) live in exactly one place instead of being
// duplicated across the repo and service decorator packages.
//
// It sits in the foundation tier and depends only on core (interfaces + errors) +
// stdlib, so both the repos and services decorator packages can build a Chain from
// the same middlewares.
package decorate

import "context"

// OpMiddleware wraps the execution of one named operation with a single cross-cutting
// concern. next runs the inner operation (the next middleware, or the operation body
// at the innermost layer); a middleware may run code before/after next, short-circuit
// it (e.g. an open circuit breaker), or run it more than once (e.g. retry). The op
// string is the operation name used for span/metric/log labels.
//
// Middlewares are composed by Chain, applied outermost-first.
//
// interface-composition exemption — decoration mechanism primitive: a WrapOp
// cross-cutting wrapper contract, not an id-CRUD data-access surface, so it embeds no
// foundation generic.
type OpMiddleware interface {
	// WrapOp runs next under this middleware's concern for the named operation.
	WrapOp(ctx context.Context, op string, next func(context.Context) error) error
}

// MiddlewareFunc adapts a plain function to OpMiddleware, so a concern that needs no
// state can be written as a function literal.
type MiddlewareFunc func(
	ctx context.Context,
	op string,
	next func(context.Context) error,
) error

// WrapOp calls the underlying function.
func (f MiddlewareFunc) WrapOp(
	ctx context.Context,
	op string,
	next func(context.Context) error,
) error {
	return f(ctx, op, next)
}
