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

// Base builds decorated jobs. The zero value applies recovery only, so Base{} is a
// working undecorated builder.
type Base struct {
	// decs resolves each job's decoration by its own name; when unset, every job shares
	// deps.
	decs *decorate.Decorations

	// prefix qualifies each job's name in the decoration config ("events" makes a job
	// "sweep" the component "events.sweep").
	prefix string

	// deps are every job's decoration when decs is unset.
	deps decorate.Deps
}

// NewBase returns a Base that decorates all its jobs with deps.
func NewBase(deps decorate.Deps) Base { return Base{deps: deps} }

// NewBaseFrom returns a Base whose jobs each take their own decoration from decs: a job
// named name is the job-tier component prefix+"."+name, so the config can tune one job,
// or every job under prefix.
func NewBaseFrom(decs decorate.Decorations, prefix string) Base {
	return Base{decs: &decs, prefix: prefix}
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

// Wrap decorates an existing job, keeping its metadata.
func (b Base) Wrap(j interfaces.AnyJob) interfaces.AnyJob {
	return b.job(j.Meta(), j.Execute)
}

// Named returns the Base whose jobs sit beneath sub: from a Base of prefix "events",
// Named("sweep") makes a job "reap" the component "events.sweep.reap". A Base built by
// NewBase shares one decoration and is returned unchanged.
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

// job binds meta and run to the job tier's subject for meta.Name.
func (b Base) job(meta types.JobMeta, run Run) interfaces.AnyJob {
	return decorated{meta: meta, run: run, op: b.subject(meta.Name)}
}

// subject returns the decorated Subject of the job named name.
func (b Base) subject(name string) decorate.Subject {
	if b.decs == nil {
		return Tier().Subject(name, b.deps)
	}
	key := name
	if b.prefix != "" {
		key = b.prefix + "." + name
	}
	return b.decs.Subject(Tier(), key)
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
