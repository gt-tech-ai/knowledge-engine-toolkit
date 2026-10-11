// Package workflow provides the base workflow implementation.
//
// BaseWorkflow wraps an orchestration function and implements the
// Workflow[In, Out] interface from core. It serves as the innermost layer
// in a decorator chain — decorators (logging, metrics) wrap BaseWorkflow
// to add cross-cutting concerns without modifying the orchestration logic.
//
// Unlike pipelines (pure stateless transformations), workflows coordinate
// stateful operations across services, pipelines, repositories, and caches.
package workflow

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Compile-time interface compliance check.
var _ interfaces.Workflow[string, string] = (*BaseWorkflow[string, string])(nil)

// BaseWorkflow wraps an orchestration function and implements the
// Workflow[In, Out] interface. It provides a functional escape hatch for
// simple workflows, though workflows are typically stateful (holding
// service/pipeline/cache references).
type BaseWorkflow[In any, Out any] struct {
	// fn is the wrapped orchestration function.
	fn func(context.Context, In) (Out, error)
}

// NewBaseWorkflow creates a new BaseWorkflow wrapping the given function.
// Panics if fn is nil.
func NewBaseWorkflow[In, Out any](
	fn func(context.Context, In) (Out, error),
) *BaseWorkflow[In, Out] {
	if fn == nil {
		panic("workflow: fn must not be nil")
	}
	return &BaseWorkflow[In, Out]{fn: fn}
}

// Execute runs the wrapped orchestration function.
// Context is passed through for cancellation and tracing support.
func (w *BaseWorkflow[In, Out]) Execute(ctx context.Context, input In) (Out, error) {
	return w.fn(ctx, input)
}
