package decorate

import (
	"context"
	"slices"
)

// chain composes several OpMiddleware into one, applied outermost-first: for
// Chain(a, b, c), a call runs a → b → c → op → c → b → a. It is unexported; callers
// hold it through the OpMiddleware interface returned by Chain.
type chain struct {
	// mws are the middlewares, index 0 outermost.
	mws []OpMiddleware
}

// Chain composes middlewares into a single OpMiddleware applied outermost-first
// (the first argument is the outermost layer). Each tier builds its Chain once at the
// composition root in its documented order (ARCHITECTURE.md#decorator-order lists each
// tier's stack; e.g. recovery outermost, with Tracing → Metrics → Logging ahead of the
// tier's resilience layers). Chain() with no middlewares is a pure passthrough.
func Chain(mws ...OpMiddleware) OpMiddleware {
	// Copy so a later mutation of the caller's slice can't reorder the chain.
	cp := make([]OpMiddleware, len(mws))
	copy(cp, mws)
	return chain{mws: cp}
}

// WrapOp applies the composed middlewares around next, outermost-first, by folding
// from the innermost middleware outward so index 0 ends up wrapping all the others.
func (c chain) WrapOp(
	ctx context.Context,
	op string,
	next func(context.Context) error,
) error {
	wrapped := next
	// Fold from the innermost middleware outward so index 0 ends up outermost. Go 1.22+
	// per-iteration loop variables make mw/inner fresh each step (no capture bug).
	for _, mw := range slices.Backward(c.mws) {
		inner := wrapped
		wrapped = func(ctx context.Context) error {
			return mw.WrapOp(ctx, op, inner)
		}
	}
	return wrapped(ctx)
}
