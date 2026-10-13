package pipeline

import (
	"context"

	"golang.org/x/sync/errgroup"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/pipelines/pipeline/decorators"
)

// Base builds a consumer's decorated pipeline stages, which compose with
// BasePipeline.Then into multi-stage pipelines. Each stage always recovers a panic as
// an INTERNAL error and, when the pipeline tier is observed, is one span, one metric
// series and one log line. The zero Base builds unobserved stages.
type Base struct {
	// obs is the pipeline tier's observability; the zero value observes nothing.
	obs decorate.Observability
}

// NewBase builds the Base whose stages are observed by obs.
func NewBase(obs decorate.Observability) Base {
	return Base{obs: obs}
}

// Stage wraps fn as one decorated pipeline stage named name.
func (b Base) Stage[In, Out any](
	name string,
	fn func(context.Context, In) (Out, error),
) *BasePipeline[In, Out] {
	decorated := decorators.NewBuilder(NewBasePipeline(fn), name).
		WithLogging(b.obs.Logger).
		WithMetrics(b.obs.Metrics).
		WithTracing(b.obs.Tracer).
		WithRecovery().
		Build()
	return NewBasePipeline(decorated.Execute)
}

// FanOut is one decorated stage named name that runs every stage on the same input
// concurrently and returns their outputs in stage order. The first error cancels the
// context the other stages run on and is returned; with no stages it returns an empty
// result.
func (b Base) FanOut[In, Out any](
	name string,
	stages ...interfaces.Pipeline[In, Out],
) *BasePipeline[In, []Out] {
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
