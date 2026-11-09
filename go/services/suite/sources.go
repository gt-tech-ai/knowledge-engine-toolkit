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
		out = append(out, runEntry(tools, cb, &runs[i]))
	}

	return out
}

// runEntry is the catalog entry of one tool run, grouped by its verb when it names
// no group.
func runEntry(
	tools interfaces.ToolRunner,
	codebase interfaces.Codebase,
	rRef *types.ToolRun,
) Entry {
	toolRun := *rRef

	group := toolRun.Group
	if group == "" {
		group = toolRun.Verb
	}

	var shells []string

	if toolRun.Tool != "" {
		shells = []string{toolRun.Tool}
	}

	return Entry{
		Name:  toolRun.Name,
		Verb:  toolRun.Verb,
		Group: group,
		Kind:  KindRun,
		Tags:  toolRun.Tags,
		Tools: shells,
		Job: func(env Env) interfaces.AnyJob {
			return Build(env, toolRun.Verb, toolRun.Name, group, func(
				ctx context.Context, _ interfaces.CommandRunner, _ string,
			) (types.StepResults, error) {
				return runStepResults(ctx, tools, codebase, &toolRun, env.Opts), nil
			})
		},
	}
}

// runStepResults runs the tool, reporting a run error as one failed step.
func runStepResults(
	ctx context.Context,
	tools interfaces.ToolRunner,
	cb interfaces.Codebase,
	r *types.ToolRun,
	opts types.RunOptions,
) types.StepResults {
	res, err := tools.Run(ctx, cb, r, opts)
	if err != nil {
		return types.OneStep(r.Name, types.StatusFail, err)
	}

	return res
}

// StepEnv is what a registered step runs against.
type StepEnv struct {
	// Env is the suite environment.
	Env

	// Runner runs the step's commands (the gate's runner).
	Runner interfaces.CommandRunner

	// Root is the repository root (the gate's root).
	Root string
}

// Step is Go work a suite runs beside its rows, selected like a row by its verb (the
// prefix of its name before the first dot) and tags.
type Step struct {
	// Run is the step's work. It takes env by pointer: a StepEnv carries the whole suite
	// environment and is too large to copy per call.
	Run func(ctx context.Context, env *StepEnv) (types.StepResults, error)

	// Group is the phase the step runs in.
	Group string

	// Tags are matched against the suite's selection like a row's.
	Tags []string

	// Tools are the binaries the step shells, for provisioning.
	Tools []string
}

// Steps returns one entry per registered step, keyed <verb>.<step> (lint.helm). A suite
// runs a step entry only when its Steps list names it.
func Steps(steps map[string]Step) []Entry {
	out := make([]Entry, 0, len(steps))
	for name, step := range steps {
		verb, _, _ := strings.Cut(name, ".")

		out = append(out, Entry{
			Name: name, Verb: verb, Group: step.Group, Kind: KindStep, Tags: step.Tags,
			Tools: step.Tools,
			Job: func(env Env) interfaces.AnyJob {
				return Build(env, verb, name, step.Group, func(
					ctx context.Context, runner interfaces.CommandRunner, root string,
				) (types.StepResults, error) {
					return step.Run(ctx, &StepEnv{Runner: runner, Env: env, Root: root})
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
// findings, one per line, are the step's error and its detail the verdict's. Under
// strict run options a skipped guard (its tool absent) fails; a cached skip never does.
func Guards(gs []interfaces.Guard, cb interfaces.Codebase) []Entry {
	out := make([]Entry, 0, len(gs))
	for _, g := range gs {
		out = append(out, guardEntry(g, cb))
	}

	return out
}

// guardEntry is the catalog entry of one guard.
func guardEntry(guard interfaces.Guard, codebase interfaces.Codebase) Entry {
	entryMeta := guard.Meta()

	return Entry{
		Name:  entryMeta.Name,
		Verb:  entryMeta.Verb,
		Group: entryMeta.Group,
		Kind:  KindGuard,
		Tags:  entryMeta.Tags,
		Tools: entryMeta.Tools,
		Job: func(env Env) interfaces.AnyJob {
			return Build(env, entryMeta.Verb, entryMeta.Name, entryMeta.Group, func(
				ctx context.Context, _ interfaces.CommandRunner, _ string,
			) (types.StepResults, error) {
				step := guardStep(
					guard.Run(ctx, codebase),
					entryMeta.Name,
					entryMeta.Group,
					&env.Opts,
				)

				return types.StepResults{step}, nil
			})
		},
	}
}

// guardStep turns a verdict into a step. Strict mode fails a skip unless it came from
// the cache; a failure always carries an error text.
func guardStep(
	verdict types.Verdict,
	name, group string,
	opts *types.RunOptions,
) types.StepResult {
	if opts.Strict && verdict.Status == types.StatusSkip &&
		verdict.Detail != types.CachedDetail {
		verdict.Status = types.StatusFail
	}

	step := types.StepResult{
		Name:     name,
		Group:    group,
		Status:   verdict.Status,
		Error:    "",
		Detail:   nil,
		Children: nil,
		Duration: 0,
		Tests:    0,
	}
	if verdict.Status != types.StatusFail {
		step.Detail = []byte(verdict.Detail)
	} else if step.Error = strings.TrimSpace(verdict.Detail); step.Error == "" {
		step.Error = "failed"
	}

	return step
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
		procedure := procs[i]
		group := procedure.Group

		if group == "" {
			group = procedure.Name
		}

		out = append(out, Entry{
			Name: procedure.Name, Verb: verb, Group: group, Kind: KindProcedure,
			Job: func(env Env) interfaces.AnyJob {
				return Build(env, verb, procedure.Name, group, func(
					ctx context.Context, _ interfaces.CommandRunner, _ string,
				) (types.StepResults, error) {
					return eng.Run(ctx, procedure.Name, input, env.Opts)
				})
			},
			Tags:  nil,
			Tools: nil,
		})
	}

	return out
}
