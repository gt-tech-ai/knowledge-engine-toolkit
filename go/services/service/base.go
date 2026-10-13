package service

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Base is the embeddable core of a consumer's hand-written service: it carries the
// service tier's operation chain (opChain: recovery → tracing → metrics → logging →
// timeout), and its promoted Exec runs each custom operation through it. Authorization
// stays at the transport edge, so the chain carries none. The zero Base runs operations
// undecorated.
type Base struct {
	// ops is the operation chain; nil runs an operation undecorated.
	ops decorate.OpMiddleware

	// name labels the service in the chain's logs, metrics and spans.
	name string
}

// NewBase builds the Base for the service named name: obs supplies the service tier's
// observability (the zero value observes nothing) and timeout bounds each operation
// (zero means none).
func NewBase(name string, obs decorate.Observability, timeout time.Duration) Base {
	return Base{
		ops:  opChain(name, timeout, obs.Logger, obs.Metrics, obs.Tracer, nil),
		name: name,
	}
}

// Name returns the service's label.
func (b Base) Name() string { return b.name }

// Exec runs fn as the operation named op through the service's chain and returns its
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
