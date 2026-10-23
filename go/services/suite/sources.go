package suite

import (
	"context"
	"slices"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// KindRun marks a tool-run entry.
const KindRun = "run"

// Runs returns one entry per tool-run row, run by tools over cb. A row's group is its
// Group, else its Verb; a tool-runner error is one failed step named after the row, so a
// row that cannot run fails its own step instead of the gate.
func Runs(
	tools interfaces.ToolRunner,
	cb interfaces.Codebase,
	runs []types.ToolRun,
) []Entry {
	out := make([]Entry, 0, len(runs))
	for i := range runs {
		r := runs[i]
		group := r.Group
		if group == "" {
			group = r.Verb
		}
		out = append(out, Entry{
			Name: r.Name, Verb: r.Verb, Group: group, Kind: KindRun, Tags: r.Tags,
			Job: func(env Env) interfaces.AnyJob {
				return Build(env, r.Verb, r.Name, group, func(
					ctx context.Context, _ interfaces.CommandRunner, _ string,
				) (types.StepResults, error) {
					res, err := tools.Run(ctx, cb, r, env.Opts)
					if err != nil {
						return types.OneStep(r.Name, types.StatusFail, err), nil
					}
					return res, nil
				})
			},
		})
	}
	return out
}

// StepEnv is what a registered step runs against.
type StepEnv struct {
	// Runner runs the step's commands (the gate's runner).
	Runner interfaces.CommandRunner

	// Env is the suite environment.
	Env

	// Root is the repository root (the gate's root).
	Root string
}

// Step is Go work a suite runs beside its rows, selected like a row by its verb (the
// prefix of its name before the first dot) and tags.
type Step struct {
	// Run is the step's work.
	Run func(ctx context.Context, env StepEnv) (types.StepResults, error)

	// Group is the phase the step runs in.
	Group string

	// Tags are matched against the suite's selection like a row's.
	Tags []string
}

// Steps returns one entry per registered step, keyed <verb>.<step> (lint.helm). A suite
// runs a step entry only when its Steps list names it.
func Steps(steps map[string]Step) []Entry {
	out := make([]Entry, 0, len(steps))
	for name, st := range steps {
		verb, _, _ := strings.Cut(name, ".")
		out = append(out, Entry{
			Name: name, Verb: verb, Group: st.Group, Kind: KindStep, Tags: st.Tags,
			Job: func(env Env) interfaces.AnyJob {
				return Build(env, verb, name, st.Group, func(
					ctx context.Context, runner interfaces.CommandRunner, root string,
				) (types.StepResults, error) {
					return st.Run(ctx, StepEnv{Runner: runner, Env: env, Root: root})
				})
			},
		})
	}
	slices.SortFunc(out, func(a, b Entry) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// KindGuard marks a guard entry.
const KindGuard = "guard"

// Guards returns one entry per guard, each run over cb as one step: a failing verdict's
// findings, one per line, are the step's error and its detail the verdict's.
func Guards(gs []interfaces.Guard, cb interfaces.Codebase) []Entry {
	out := make([]Entry, 0, len(gs))
	for _, g := range gs {
		m := g.Meta()
		out = append(out, Entry{
			Name: m.Name, Verb: m.Verb, Group: m.Group, Kind: KindGuard, Tags: m.Tags,
			Job: func(env Env) interfaces.AnyJob {
				return Build(env, m.Verb, m.Name, m.Group, func(
					ctx context.Context, _ interfaces.CommandRunner, _ string,
				) (types.StepResults, error) {
					v := g.Run(ctx, cb)
					step := types.StepResult{
						Name:   m.Name,
						Group:  m.Group,
						Status: v.Status,
					}
					if v.Status == types.StatusFail {
						step.Error = strings.TrimSpace(v.Detail)
						if step.Error == "" {
							step.Error = "failed"
						}
					}
					return types.StepResults{step}, nil
				})
			},
		})
	}
	return out
}

// KindProcedure marks a procedure entry.
const KindProcedure = "procedure"

// Procedures returns one entry per procedure, run by eng with input; verb is the command
// that owns them. A procedure's group is its Group, else its Name.
func Procedures(
	eng interfaces.ProcedureRunner,
	procs []types.Procedure,
	verb string,
	input map[string]any,
) []Entry {
	out := make([]Entry, 0, len(procs))
	for i := range procs {
		p := procs[i]
		group := p.Group
		if group == "" {
			group = p.Name
		}
		out = append(out, Entry{
			Name: p.Name, Verb: verb, Group: group, Kind: KindProcedure,
			Job: func(env Env) interfaces.AnyJob {
				return Build(env, verb, p.Name, group, func(
					ctx context.Context, _ interfaces.CommandRunner, _ string,
				) (types.StepResults, error) {
					return eng.Run(ctx, p.Name, input, env.Opts)
				})
			},
		})
	}
	return out
}
