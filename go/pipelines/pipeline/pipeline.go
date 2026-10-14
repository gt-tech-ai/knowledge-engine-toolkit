// Package pipeline is the pipeline tier: a Base builds a consumer's decorated
// pipeline stages from functions, and stages compose with Stage.Then into a staged
// pipeline or run side by side with Base.FanOut. Every stage is a core
// interfaces.Pipeline, decorated in the tier's order (Tier).
package pipeline

import (
	"context"

	"golang.org/x/sync/errgroup"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Tier is the pipeline tier's decoration: recovery → logging → tracing → metrics →
// timeout, outermost first.
func Tier() decorate.Tier {
	return decorate.Tier{Name: "pipeline", Order: []decorate.Layer{
		decorate.LayerRecovery,
		decorate.LayerLogging,
		decorate.LayerTracing,
		decorate.LayerMetrics,
		decorate.LayerTimeout,
	}}
}

// Compile-time interface compliance check.
var _ interfaces.Pipeline[string, string] = Stage[string, string](nil)

// Stage is one pipeline stage: a function that is an interfaces.Pipeline.
type Stage[In, Out any] func(context.Context, In) (Out, error)

// Execute runs the stage.
func (s Stage[In, Out]) Execute(ctx context.Context, input In) (Out, error) {
	return s(ctx, input)
}

// Then composes s with next into one stage: s's output is next's input. An error from
// s is returned unchanged and next does not run. Each stage keeps its own decorators,
// so a slow or failing stage is visible by name. A nil next panics, as a wiring bug.
func (s Stage[In, Out]) Then[C any](next interfaces.Pipeline[Out, C]) Stage[In, C] {
	if next == nil {
		panic("pipeline: Then stage must not be nil")
	}
	return func(ctx context.Context, input In) (C, error) {
		mid, err := s(ctx, input)
		if err != nil {
			var zero C
			return zero, err
		}
		return next.Execute(ctx, mid)
	}
}

// Base builds a consumer's decorated stages over the pipeline tier's collaborators.
// The zero Base builds stages that only recover a panic.
type Base struct {
	// deps backs every stage's decoration.
	deps decorate.Deps
}

// NewBase builds the Base whose stages are decorated with deps.
func NewBase(deps decorate.Deps) Base {
	return Base{deps: deps}
}

// Stage wraps fn as one decorated stage named name; it runs as name's "Execute"
// operation. A nil fn panics, as a wiring bug.
func (b Base) Stage[In, Out any](
	name string,
	fn func(context.Context, In) (Out, error),
) Stage[In, Out] {
	if fn == nil {
		panic("pipeline: stage fn must not be nil")
	}
	subject := Tier().Subject(name, b.deps)
	return func(ctx context.Context, input In) (Out, error) {
		return subject.Exec(ctx, "Execute", func(ctx context.Context) (Out, error) {
			return fn(ctx, input)
		})
	}
}

// FanOut is one decorated stage named name that runs every stage on the same input
// concurrently and returns their outputs in stage order. The first error cancels the
// context the other stages run on and is returned; with no stages it returns an empty
// result.
func (b Base) FanOut[In, Out any](
	name string,
	stages ...interfaces.Pipeline[In, Out],
) Stage[In, []Out] {
	return b.Stage(name, func(ctx context.Context, input In) ([]Out, error) {
		outs := make([]Out, len(stages))
		g, gctx := errgroup.WithContext(ctx)
		for i, stage := range stages {
			g.Go(func() error {
				out, err := stage.Execute(gctx, input)
				outs[i] = out
				return err
			})
		}
		if err := g.Wait(); err != nil {
			return nil, err
		}
		return outs, nil
	})
}
