package decorate

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// NewCircuitBreaker returns a middleware that runs the operation through the circuit
// breaker, so a run of failures opens the breaker and short-circuits subsequent calls.
func NewCircuitBreaker(cb interfaces.CircuitBreaker) OpMiddleware {
	return MiddlewareFunc(
		func(ctx context.Context, _ string, next func(context.Context) error) error {
			return cb.Execute(func() error { return next(ctx) })
		},
	)
}
