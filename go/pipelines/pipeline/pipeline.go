// Package pipeline provides the base pipeline implementation.
//
// BasePipeline wraps a stateless transformation function and implements the
// Pipeline[In, Out] interface from core. It serves as the innermost layer
// in a decorator chain — decorators (logging, metrics) wrap BasePipeline
// to add cross-cutting concerns without modifying the transformation logic. Its Then
// method composes it with a next stage into one staged pipeline, each stage keeping
// its decorators; Base builds the decorated stages and the concurrent FanOut stage.
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

// Then composes p with next into one pipeline: p's output is next's input. An error
// from p is returned unchanged and next does not run. Each stage keeps its own
// decorators, so stages built with Base.Stage give one span and one metric series per
// stage. A nil next panics, as a wiring bug.
func (p *BasePipeline[In, Out]) Then[C any](
	next interfaces.Pipeline[Out, C],
) *BasePipeline[In, C] {
	if next == nil {
		panic("pipeline: Then stage must not be nil")
	}
	return NewBasePipeline(func(ctx context.Context, input In) (C, error) {
		mid, err := p.fn(ctx, input)
		if err != nil {
			var zero C
			return zero, err
		}
		return next.Execute(ctx, mid)
	})
}
