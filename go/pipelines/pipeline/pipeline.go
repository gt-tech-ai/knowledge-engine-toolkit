// Package pipeline provides the base pipeline implementation.
//
// BasePipeline wraps a stateless transformation function and implements the
// Pipeline[In, Out] interface from core. It serves as the innermost layer
// in a decorator chain — decorators (logging, metrics) wrap BasePipeline
// to add cross-cutting concerns without modifying the transformation logic. Then
// composes two pipelines into one staged pipeline, each stage keeping its decorators.
package pipeline

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Compile-time interface compliance check.
var _ interfaces.Pipeline[string, string] = (*BasePipeline[string, string])(nil)

// BasePipeline wraps a stateless transformation function and implements
// the Pipeline[In, Out] interface. It provides a simple way to create
// pipelines from pure functions without requiring a struct definition.
type BasePipeline[In any, Out any] struct {
	// fn is the wrapped transformation function.
	fn func(context.Context, In) (Out, error)
}

// NewBasePipeline creates a new BasePipeline wrapping the given function.
// Panics if fn is nil.
func NewBasePipeline[In, Out any](
	fn func(context.Context, In) (Out, error),
) *BasePipeline[In, Out] {
	if fn == nil {
		panic("pipeline: fn must not be nil")
	}
	return &BasePipeline[In, Out]{fn: fn}
}

// Execute transforms the input by calling the wrapped function.
// Context is passed through for cancellation and tracing support.
func (p *BasePipeline[In, Out]) Execute(ctx context.Context, input In) (Out, error) {
	return p.fn(ctx, input)
}
