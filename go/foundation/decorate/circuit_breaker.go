package decorate

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/circuitbreaker"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/series"
)

// NewCircuitBreaker returns a middleware that runs the operation through the circuit
// breaker, so a run of failures opens the breaker and short-circuits subsequent calls.
// A call the breaker refuses (circuitbreaker.IsOpen) is reported through obs as a
// breaker_open rejection; the dependency's own failures are not.
func NewCircuitBreaker(cb interfaces.CircuitBreaker, obs Observer) OpMiddleware {
	return MiddlewareFunc(
		func(ctx context.Context, operation string, next func(context.Context) error) error {
			err := cb.Execute(func() error { return next(ctx) })
			if circuitbreaker.IsOpen(err) {
				obs.Recorder.Rejected(operation, series.ReasonBreakerOpen)
			}

			return err
		},
	)
}
