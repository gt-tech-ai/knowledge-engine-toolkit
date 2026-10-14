// Package job builds interfaces.AnyJob values: Base decorates every job it builds
// with the job tier (recovery, logging, tracing, metrics, timeout) once, so a job
// is a function plus its name and group, never a hand-written AnyJob type.
package job

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Tier is the job decoration tier: recovery outermost, then logging, tracing,
// metrics, and the per-job timeout. A job runs as the operation "Execute".
func Tier() decorate.Tier {
	return decorate.Tier{Name: "job", Order: []decorate.Layer{
		decorate.LayerRecovery,
		decorate.LayerLogging,
		decorate.LayerTracing,
		decorate.LayerMetrics,
		decorate.LayerTimeout,
	}}
}

// Run is a job's work: it executes against runner under root and returns the
// job's step results.
type Run func(
	ctx context.Context,
	runner interfaces.CommandRunner,
	root string,
) (types.StepResults, error)

// Base builds decorated jobs from one set of decoration dependencies. The zero
// value applies recovery only, so Base{} is a working undecorated builder.
type Base struct {
	// deps are the job tier's observability and timeout dependencies.
	deps decorate.Deps
}

// NewBase returns a Base that decorates its jobs with deps.
func NewBase(deps decorate.Deps) Base { return Base{deps: deps} }

// Func builds the job name in group that runs run.
func (b Base) Func(name, group string, run Run) interfaces.AnyJob {
	return b.job(types.JobMeta{Name: name, Group: group}, run)
}

// Runnable adapts r — a job that needs no runner or root — into a decorated job
// carrying r's metadata.
func (b Base) Runnable(r interfaces.Runnable) interfaces.AnyJob {
	return b.job(
		r.Meta(),
		func(
			ctx context.Context, _ interfaces.CommandRunner, _ string,
		) (types.StepResults, error) {
			return r.Run(ctx)
		},
	)
}

// Wrap decorates an existing job, keeping its metadata.
func (b Base) Wrap(j interfaces.AnyJob) interfaces.AnyJob {
	return b.job(j.Meta(), j.Execute)
}

// job binds meta and run to the job tier's subject for meta.Name.
func (b Base) job(meta types.JobMeta, run Run) interfaces.AnyJob {
	return decorated{meta: meta, run: run, op: Tier().Subject(meta.Name, b.deps)}
}

// decorated is the one AnyJob implementation every Base method returns.
type decorated struct {
	// run is the job's work.
	run Run

	// op runs run through the job tier.
	op decorate.Subject

	// meta is the job's identity.
	meta types.JobMeta
}

var _ interfaces.AnyJob = decorated{}

// Meta returns the job's identity.
func (d decorated) Meta() types.JobMeta { return d.meta }

// Execute runs the job through the job tier as the operation "Execute".
func (d decorated) Execute(
	ctx context.Context,
	runner interfaces.CommandRunner,
	root string,
) (types.StepResults, error) {
	return d.op.Exec(
		ctx,
		"Execute",
		func(ctx context.Context) (types.StepResults, error) {
			return d.run(ctx, runner, root)
		},
	)
}
