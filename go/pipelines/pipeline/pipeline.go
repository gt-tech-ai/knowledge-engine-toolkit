// Package pipeline is the pipeline tier: a Base, built from the pipeline's contract, is
// the embeddable core of a consumer's pipeline, running each operation through the
// tier's decoration (Tier) with the promoted Exec or as a stage (Base.Stage) that
// composes with Stage.Then or runs side by side with Base.FanOut. Its role compositions
// (roles.go) decorate a service's roles. Every stage is a core interfaces.Pipeline.
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

// Base is the embeddable core of a pipeline named by its Subject: Name and Exec run one
// of its operations through the pipeline tier, and Stage and FanOut build an operation as
// a composable stage. Each operation is a method of the pipeline's contract, decorated
// under its own name. The zero Base runs stages undecorated.
type Base struct {
	// Subject runs the pipeline's operations through the tier; it supplies Name and Exec.
	decorate.Subject
}

// NewBase builds the Base for the pipeline named name whose operations are the methods of
// its contract P (an interface), decorated from decs: the pipeline as the pipeline-tier
// component name, and each operation op as name+"."+op, so the config can tune the
// pipeline or one of its stages. A contract that is not an interface, or a decoration
// that fails to resolve, is an error.
func NewBase[P any](decs decorate.Decorations, name string) (Base, error) {
	subject, err := decs.SubjectOf[P](Tier(), name)
	if err != nil {
		return Base{}, err
	}
	return Base{Subject: subject}, nil
}

// Stage wraps fn as the decorated operation op, a stage that composes with Then. A nil
// fn panics, as a wiring bug.
func (b Base) Stage[In, Out any](
	op string,
	fn func(context.Context, In) (Out, error),
) Stage[In, Out] {
	if fn == nil {
		panic("pipeline: stage fn must not be nil")
	}
	return func(ctx context.Context, input In) (Out, error) {
		return b.Exec(ctx, op, func(ctx context.Context) (Out, error) {
			return fn(ctx, input)
		})
	}
}

// FanOut is the decorated operation op, a stage that runs every stage on the same input
// concurrently and returns their outputs in stage order. The first error cancels the
// context the other stages run on and is returned; with no stages it returns an empty
// result.
func (b Base) FanOut[In, Out any](
	op string,
	stages ...interfaces.Pipeline[In, Out],
) Stage[In, []Out] {
	return b.Stage(op, func(ctx context.Context, input In) ([]Out, error) {
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

// Unit adapts an error-only step to a stage function, whose result is the empty value.
func Unit[In any](
	step func(context.Context, In) error,
) func(context.Context, In) (struct{}, error) {
	return func(ctx context.Context, in In) (struct{}, error) {
		return struct{}{}, step(ctx, in)
	}
}
