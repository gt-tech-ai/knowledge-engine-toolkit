// Package job is the job tier: a Base, one per job, decorates the job it builds with the
// job tier (recovery, logging, tracing, metrics, timeout), so a job is a function plus
// its name and group, never a hand-written AnyJob type.
package job

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Tier is the job decoration tier: recovery outermost, then logging, tracing,
// metrics, and the per-job timeout. A job runs as the operation "execute".
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

// Base is the embeddable core of one job named by its Subject: the job runs through the
// job tier as the operation "execute", the snake_case name of interfaces.AnyJob's
// Execute. Func, Local and Wrap build the job; build one job per Base, so each job is
// its own component in the config. The zero Base builds an undecorated job.
type Base struct {
	// Subject runs the job through the tier; it supplies Name and Exec.
	decorate.Subject
}

// NewBase builds the Base for the job named name, decorated from decs as the job-tier
// component name (its operation as name+".execute"). A decoration that fails to resolve
// is an error.
func NewBase(decs decorate.Decorations, name string) (Base, error) {
	subject, err := decs.SubjectOf[interfaces.AnyJob](Tier(), name)
	if err != nil {
		return Base{}, err
	}
	return Base{Subject: subject}, nil
}

// Func builds the job name in group that runs run.
func (b Base) Func(name, group string, run Run) interfaces.AnyJob {
	return b.job(types.JobMeta{Name: name, Group: group}, run)
}

// Local builds the job name in group that runs fn, in-process work (a seeder, a
// sync) that needs no command runner or root; a gate of Local jobs runs with
// gate.RunLocal.
func (b Base) Local(
	name, group string,
	fn func(context.Context) (types.StepResults, error),
) interfaces.AnyJob {
	return b.Func(name, group, func(
		ctx context.Context, _ interfaces.CommandRunner, _ string,
	) (types.StepResults, error) {
		return fn(ctx)
	})
}

// Check builds the job name in group that runs one pass/fail check: one step named
// name, Pass when run returns nil, otherwise Fail carrying the error's message.
func (b Base) Check(
	name, group string,
	run func(context.Context) error,
) interfaces.AnyJob {
	return b.Local(name, group, func(ctx context.Context) (types.StepResults, error) {
		sr := types.StepResult{Name: name, Status: types.StatusPass}
		if err := run(ctx); err != nil {
			sr.Status, sr.Error = types.StatusFail, err.Error()
		}
		return types.StepResults{sr}, nil
	})
}

// Wrap decorates an existing job, keeping its metadata.
func (b Base) Wrap(j interfaces.AnyJob) interfaces.AnyJob {
	return b.job(j.Meta(), j.Execute)
}

// job binds meta and run to the job tier.
func (b Base) job(meta types.JobMeta, run Run) interfaces.AnyJob {
	return decorated{meta: meta, run: run, op: b.Subject}
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

// Execute runs the job through the job tier as the operation "execute".
func (d decorated) Execute(
	ctx context.Context,
	runner interfaces.CommandRunner,
	root string,
) (types.StepResults, error) {
	return d.op.Exec(
		ctx,
		"execute",
		func(ctx context.Context) (types.StepResults, error) {
			return d.run(ctx, runner, root)
		},
	)
}
