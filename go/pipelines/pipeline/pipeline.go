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
	// decs resolves each stage's decoration by its own name; when unset, every stage
	// shares deps.
	decs *decorate.Decorations

	// prefix qualifies each stage's name ("api.documents" makes a stage "create" the
	// component "api.documents.create").
	prefix string

	// deps backs every stage's decoration when decs is unset.
	deps decorate.Deps
}

// NewBase builds the Base whose stages are all decorated with deps.
func NewBase(deps decorate.Deps) Base {
	return Base{deps: deps}
}

// NewBaseFrom builds the Base whose stages each take their own decoration from decs:
// a stage named name is the pipeline-tier component prefix+"."+name, so the config
// can tune one stage, or every stage under prefix.
func NewBaseFrom(decs decorate.Decorations, prefix string) Base {
	return Base{decs: &decs, prefix: prefix}
}

// Named returns the Base whose stages sit beneath sub: from a Base of prefix "api",
// Named("documents") makes a stage "create" the component "api.documents.create". A
// pipeline set's constructor scopes its stages with it, so two sets may each have a stage
// of one name. A Base built by NewBase shares one decoration and is returned unchanged.
func (b Base) Named(sub string) Base {
	if b.decs == nil {
		return b
	}
	if b.prefix != "" {
		sub = b.prefix + "." + sub
	}
	b.prefix = sub
	return b
}

// Subject returns the decorated Subject of the pipeline-tier component named name (under
// the Base's prefix), for a pipeline that is not built from stage functions — a stream
// relay, say — to run its operations through the tier as a stage would.
func (b Base) Subject(name string) decorate.Subject {
	if b.decs == nil {
		return Tier().Subject(name, b.deps)
	}
	if b.prefix != "" {
		name = b.prefix + "." + name
	}
	return b.decs.Subject(Tier(), name)
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
	subject := b.Subject(name)
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

// Resource is the pipeline over svc whose roles each run as one decorated operation of
// the pipeline named name.
func (b Base) Resource[T, P any, ID comparable, C, U any](
	name string,
	svc interfaces.Resource[T, P, ID, C, U],
) interfaces.Resource[T, P, ID, C, U] {
	subject := b.Subject(name)
	return decorate.NewResource[T, P, ID, C, U](
		decorate.ResourceOps{Create: subject, Rest: subject},
		svc,
		nil,
		nil,
	)
}

// Unit adapts an error-only step to a stage function, whose result is the empty value.
func Unit[In any](
	step func(context.Context, In) error,
) func(context.Context, In) (struct{}, error) {
	return func(ctx context.Context, in In) (struct{}, error) {
		return struct{}{}, step(ctx, in)
	}
}
