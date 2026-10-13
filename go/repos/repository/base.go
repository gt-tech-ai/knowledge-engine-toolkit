package repository

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Base is the embeddable core of a consumer's hand-written repository: it carries the
// repository tier's operation chain (opChain: tracing → metrics → logging → timeout →
// circuit breaker → retry), and its promoted Exec runs each custom query (anything the
// decorated CRUD stack does not cover) through it. The zero Base runs queries
// undecorated.
type Base struct {
	// ops is the operation chain; nil runs a query undecorated.
	ops decorate.OpMiddleware

	// name labels the repository in the chain's logs, metrics and spans.
	name string
}

// NewBase builds the Base for the repository named name from deps (a nil collaborator
// or a zero timeout skips its layer).
func NewBase(name string, deps OpDeps) Base {
	return Base{
		ops: opChain(name, deps.Timeout, deps.Logger, deps.Metrics, deps.Tracer,
			deps.Retrier, deps.Breaker),
		name: name,
	}
}

// Name returns the repository's label.
func (b Base) Name() string { return b.name }

// Exec runs fn as the query named op through the repository's chain and returns its
// result.
func (b Base) Exec[R any](
	ctx context.Context,
	op string,
	fn func(context.Context) (R, error),
) (R, error) {
	if b.ops == nil {
		return fn(ctx)
	}
	return decorate.Exec(ctx, b.ops, op, fn)
}
