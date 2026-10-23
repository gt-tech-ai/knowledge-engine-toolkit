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
