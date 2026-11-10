package unit_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.yaml.in/yaml/v3"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/procedure"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/suite"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// recorder is a command runner mock that records each buffered invocation and answers
// it from out, keyed by the command line.
type recorder struct {
	// out maps a command line to its stdout; a "!" prefix makes the command fail.
	out map[string]string

	// envs maps a command line to the environment it ran with.
	envs map[string][]string

	// calls are the command lines run, in order.
	calls []string

	// streamed are the command lines run with their output streamed, in order.
	streamed []string

	// mu guards calls, streamed and envs.
	mu sync.Mutex
}

// recordStreamed is record for an invocation that streams its output.
func (r *recorder) recordStreamed(
	env []string,
	exe string,
	args []string,
) (string, bool) {
	line := strings.Join(append([]string{exe}, args...), " ")

	r.mu.Lock()

	r.streamed = append(r.streamed, line)
	r.mu.Unlock()

	return r.record(env, exe, args)
}

// record notes one invocation and answers it from out.
func (r *recorder) record(
	env []string,
	exe string,
	args []string,
) (string, bool) {
	line := strings.Join(append([]string{exe}, args...), " ")

	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls = append(r.calls, line)

	if r.envs == nil {
		r.envs = map[string][]string{}
	}

	r.envs[line] = env

	rest, failed := strings.CutPrefix(r.out[line], textBang)

	return rest, failed
}

// runner returns the mock.
func (r *recorder) runner(ctrl *gomock.Controller) *mocks.MockCommandRunner {
	commandRunner := mocks.NewMockCommandRunner(ctrl)
	commandRunner.EXPECT().
		Exists(gomock.Any()).
		DoAndReturn(func(n string) bool { return n != textAbsent }).
		AnyTimes()
	commandRunner.EXPECT().
		RunBufferedWithEnv(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
			gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, env []string, exe string,
			args ...string,
		) interfaces.CmdResult {
			out, failed := r.record(env, exe, args)
			if failed {
				return fixtures.Build(func(row *interfaces.CmdResult) {
					row.Err = apperr.Sentinel("exit 1")
					row.Stderr = []byte(out)
				})
			}

			return fixtures.Build(func(row *interfaces.CmdResult) {
				row.Stdout = []byte(out)
			})
		}).
		AnyTimes()
	commandRunner.EXPECT().
		RunWithEnv(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
			gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, env []string, exe string,
			args ...string,
		) error {
			if _, failed := r.recordStreamed(env, exe, args); failed {
				return apperr.Sentinel("exit 1")
			}

			return nil
		}).
		AnyTimes()

	return commandRunner
}

// procEngine builds procs over rec with every tool run directly by its name.
func procEngine(
	t *testing.T,
	rec *recorder,
	procs []types.Procedure,
	depsRef *procedure.Deps,
) interfaces.ProcedureRunner {
	t.Helper()

	deps := *depsRef

	ctrl := gomock.NewController(t)

	deps.Runner = rec.runner(ctrl)
	deps.Tools = func(name string) (interfaces.ToolSpec, bool) {
		return toolOf(ctrl, name), name != textUnknown
	}

	eng, err := procedure.NewFromConfig(procedure.Config{Procedures: procs}, &deps)
	require.NoError(t, err)

	return eng
}

// pruneProcedure decodes the prune fixture.
func pruneProcedure(t *testing.T) types.Procedure {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("testdata", "procedure", "prune.yaml"))
	require.NoError(t, err)

	var p types.Procedure

	require.NoError(t, yaml.Unmarshal(raw, &p))

	return p
}

// statuses maps each result's name to its status.
func statuses(res types.StepResults) map[string]types.StepStatus {
	out := map[string]types.StepStatus{}
	for _, r := range res {
		out[r.Name] = r.Status
	}

	return out
}

// TestServicesProcedure_Prune tests a real procedure from data.
//
// Why this test is important:
//   - the CLI's imperative step sequences (prune, deploy, setup) become data only if a
//     procedure reproduces their exact invocations, captures and conditions.
//
// What it tests:
//   - the prune fixture runs compose down with the splatted files and derived rmi mode,
//     removes the captured images, skips the volume removal when none are listed, and
//     asks its confirmation; under dry-run only the read-only listings run and each
//     mutating step reports its command without a question.
func TestServicesProcedure_Prune(t *testing.T) {
	t.Parallel()

	statusByName := map[string]any{
		"project": textApp, "compose_files": []string{"-f", "c.yaml"},
		"remove_all": false,
	}
	listings := map[string]string{
		textDockerImagesFilter: "a1\nb2\n",
	}
	rec := fixtures.BuildRef(func(row *recorder) {
		row.out = listings
	})
	ctrl := gomock.NewController(t)
	prompter := mocks.NewMockPrompter(ctrl)
	prompter.EXPECT().Confirm(gomock.Any(),
		"Remove every app container, image and volume?").Return(true, nil)

	eng := procEngine(
		t,
		rec,
		[]types.Procedure{pruneProcedure(t)},
		fixtures.BuildRef(func(row *procedure.Deps) {
			row.Prompter = prompter
		}),
	)

	res, err := eng.Run(
		t.Context(),
		textPrune,
		statusByName,
		stdx.Zero[types.RunOptions](),
	)

	testServicesProcedurePruneChecksResults(t, statusByName, listings, rec, res, err)
}

// testServicesProcedurePruneChecksResults continues TestServicesProcedure_Prune.
func testServicesProcedurePruneChecksResults(
	t *testing.T,
	statusByName map[string]any,
	listings map[string]string,
	rec *recorder,
	res types.StepResults,
	err error,
) {
	t.Helper()

	require.NoError(t, err)
	assert.Equal(t, []string{
		"docker compose -f c.yaml --profile gateway --profile observability down " +
			"--volumes --remove-orphans --rmi local",
		textDockerImagesFilter,
		"docker rmi -f a1 b2",
		"docker image prune -f",
		"docker volume ls -q --filter name=app",
	}, rec.calls)
	assert.Equal(t, types.StatusSkip, statuses(res)["remove volumes"])
	assert.Equal(t, textPrune, res[0].Group)

	dry := fixtures.BuildRef(func(row *recorder) {
		row.out = listings
	})

	eng := procEngine(t, dry, []types.Procedure{pruneProcedure(t)}, new(procedure.Deps))

	res, err = eng.Run(
		t.Context(),
		textPrune,
		statusByName,
		fixtures.Build(func(row *types.RunOptions) {
			row.DryRun = true
		}),
	)
	require.NoError(t, err)
	assert.Equal(t, []string{
		textDockerImagesFilter,
		"docker volume ls -q --filter name=app",
	}, dry.calls)
	assert.Equal(t, types.StatusSkip, res[2].Status)
	assert.Equal(t, "dry-run: docker rmi -f a1 b2", res[2].Error)
}

// TestServicesProcedure_ControlFlow tests loops, parallel steps, calls and Go steps.
//
// Why this test is important:
//   - procedures replace hand-written orchestration only if the control flow a CLI
//     needs (per-item loops, fan-out, sub-procedures, residual Go) composes correctly.
//
// What it tests:
//   - each runs once per item under its alias; parallel children all run and their
//     captures merge; a call runs the callee with the caller's variables; a Go step's
//     variables reach later steps; json and exit captures; an advisory failure is a
//     warning and continues and a plain failure stops; a missing tool with missing:
//     skip is skipped, as is one whose missing policy a variable set to skip.
func TestServicesProcedure_ControlFlow(t *testing.T) {
	t.Parallel()

	rec := fixtures.BuildRef(func(row *recorder) {
		row.out = map[string]string{
			"kubectl get -o json": `{"items":["x"]}`,
			textFalse:             textBoomFailure,
			textProbe:             textBang,
		}
	})
	setter := procedure.StepFunc(
		func(_ context.Context, vars procedure.Vars, _ types.RunOptions,
		) (types.StepResults, procedure.Vars, error) {
			return types.StepResults{
					fixtures.Build(func(row *types.StepResult) {
						row.Name = textGoLower
						row.Status = types.StatusPass
					}),
				},
				procedure.Vars{
					textTag:  asType[string](t, vars[textEnv]) + "-1",
					"policy": textSkip,
				}, nil
		},
	)
	eng := procEngine(
		t,
		rec,
		testServicesProcedureControlFlowTable(),
		fixtures.BuildRef(func(row *procedure.Deps) {
			row.Steps = map[string]procedure.StepFunc{textSet: setter}
		}),
	)

	res, err := eng.Run(
		t.Context(),
		textMain,
		map[string]any{
			textEnv:  textDev,
			textSvcs: []string{textAPI, textWeb},
		},
		stdx.Zero[types.RunOptions](),
	)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"helm upgrade api --set env=dev", "helm upgrade web --set env=dev",
	}, rec.calls[:2])
	assert.ElementsMatch(t, []string{"kubectl get -o json", textProbe}, rec.calls[2:4])
	assert.Equal(
		t,
		[]string{"docker tag dev-1", "echo 1 1", textFalse, textFalse},
		rec.calls[4:],
	)

	statusByName := statuses(res)
	assert.Equal(t, types.StatusPass, statusByName["deploy web"])
	assert.Equal(t, types.StatusSkip, statusByName[textAbsent])
	assert.Equal(t, types.StatusSkip, statusByName["absent-var"])
	assert.Equal(t, types.StatusWarn, statusByName[textSoft])
	assert.Equal(t, types.StatusFail, statusByName["hard"])
	assert.NotContains(t, statusByName, textNever)
}

// testServicesProcedureControlFlowTable builds the table
// TestServicesProcedure_ControlFlow runs.
func testServicesProcedureControlFlowTable() []types.Procedure {
	return []types.Procedure{
		fixtures.Build(func(row *types.Procedure) {
			row.Name = textMain
			row.Inputs = []string{textEnv, textSvcs}
			row.Steps = controlFlowMainSteps()
		}),
		fixtures.Build(func(row *types.Procedure) {
			row.Name = textSub
			row.Inputs = []string{textTag}
			row.Steps = []types.ProcStep{toolStep(textTag, textDocker, textTag, "{tag}")}
		}),
	}
}

// toolStep returns a procedure step that runs tool with args.
func toolStep(name, tool string, args ...string) types.ProcStep {
	return fixtures.Build(func(row *types.ProcStep) {
		row.Name = name
		row.Tool = tool
		row.Args = args
	})
}

// controlFlowMainSteps returns the steps of the control-flow test's main procedure.
func controlFlowMainSteps() []types.ProcStep {
	deploy := toolStep("deploy {svc}", textHelm, "upgrade", "{svc}", "--set", "env={env}")

	deploy.Each = textSvcs
	deploy.As = textSvc

	set := toolStep(textSet, "")

	set.Step = textSet

	sub := toolStep(textSub, "")

	sub.Call = textSub

	return slices.Concat(
		[]types.ProcStep{
			deploy,
			controlFlowFanStep(),
			set,
			sub,
			toolStep(textCount, textEcho, "${size(pods.items)}", "${ok}"),
		},
		controlFlowToleratedSteps(),
		[]types.ProcStep{toolStep("hard", textFalse), toolStep(textNever, textEcho)},
	)
}

// controlFlowFanStep returns the parallel step that captures pods and the probe exit.
func controlFlowFanStep() types.ProcStep {
	pods := toolStep(textPods, textKubectl, textGet, "-o", textStructuredFormat)

	pods.Capture = &types.Capture{Var: textPods, As: types.CaptureJSON}

	probe := toolStep(textProbe, textProbe)

	probe.Capture = &types.Capture{Var: textOk, As: types.CaptureExit}

	fan := toolStep("fan", "")

	fan.Parallel = []types.ProcStep{pods, probe}

	return fan
}

// controlFlowToleratedSteps returns the steps whose missing tool or failure is tolerated.
func controlFlowToleratedSteps() []types.ProcStep {
	absent := toolStep(textAbsent, textAbsent)

	absent.Missing = textSkip

	absentVar := toolStep("absent-var", textAbsent)

	absentVar.Missing = "{policy}"

	soft := toolStep(textSoft, textFalse)

	soft.Advisory = true

	return []types.ProcStep{absent, absentVar, soft}
}

// TestServicesProcedure_Refusals tests what stops a procedure with an error.
//
// Why this test is important:
//   - a declined confirmation must never run the destructive step after it, and a
//     malformed procedure must fail when it is built, not halfway through a deploy.
//
// What it tests:
//   - a declined or unanswerable confirmation is CodeCanceled before the step runs;
//     Yes skips the question; a missing variable and an unknown procedure are errors;
//     invalid steps fail NewFromConfig.
func TestServicesProcedure_Refusals(t *testing.T) {
	t.Parallel()

	confirmed := []types.Procedure{fixtures.Build(func(row *types.Procedure) {
		row.Name = textP
		row.Steps = []types.ProcStep{
			fixtures.Build(func(row *types.ProcStep) {
				row.Name = textRm
				row.Confirm = "Delete?"
				row.Tool = textRm
			}),
		}
	})}
	rec := new(recorder)
	eng := procEngine(t, rec, confirmed, new(procedure.Deps))
	_, err := eng.Run(t.Context(), textP, nil, stdx.Zero[types.RunOptions]())
	assert.Equal(t, apperr.CodeCanceled, apperr.Code(err))
	assert.Empty(t, rec.calls)

	_, err = eng.Run(
		t.Context(),
		textP,
		nil,
		fixtures.Build(func(row *types.RunOptions) {
			row.Yes = true
		}),
	)
	require.NoError(t, err)
	assert.Equal(t, []string{textRm}, rec.calls)

	_, err = eng.Run(t.Context(), textNope, nil, stdx.Zero[types.RunOptions]())
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))

	eng = procEngine(t, rec, []types.Procedure{fixtures.Build(func(row *types.Procedure) {
		row.Name = textV
		row.Steps = []types.ProcStep{
			fixtures.Build(func(row *types.ProcStep) {
				row.Name = textX
				row.Tool = textEcho
				row.Args = []string{"{missing}"}
			}),
		}
	})}, new(procedure.Deps))
	_, err = eng.Run(t.Context(), textV, nil, stdx.Zero[types.RunOptions]())
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	for name, procStep := range testServicesProcedureRefusalsTable() {
		_, err := procedure.NewFromConfig(procedure.Config{Procedures: []types.Procedure{
			fixtures.Build(func(row *types.Procedure) {
				row.Name = textP
				row.Steps = []types.ProcStep{procStep}
			}),
		}}, fixtures.BuildRef(func(row *procedure.Deps) {
			row.Tools = func(n string) (interfaces.ToolSpec, bool) {
				return nil, n != textUnknown
			}
		}))
		require.Error(t, err, name)
	}
}

// testServicesProcedureRefusalsTable builds the table TestServicesProcedure_Refusals
// runs.
func testServicesProcedureRefusalsTable() map[string]types.ProcStep {
	return map[string]types.ProcStep{
		"two kinds": fixtures.Build(func(row *types.ProcStep) {
			row.Tool = textEcho
			row.Call = textP
		}),
		"fail + tool": fixtures.Build(func(row *types.ProcStep) {
			row.Tool = textEcho
			row.Fail = "no"
		}),
		"no kind": stdx.Zero[types.ProcStep](),
		"unknown tool": fixtures.Build(func(row *types.ProcStep) {
			row.Tool = textUnknown
		}),
		"unknown step": fixtures.Build(func(row *types.ProcStep) {
			row.Step = textNope
		}),
		"unknown call": fixtures.Build(func(row *types.ProcStep) {
			row.Call = textNope
		}),
		"bad capture": fixtures.Build(func(row *types.ProcStep) {
			row.Tool = textEcho
			row.Capture = &types.Capture{Var: textV, As: "xml"}
		}),
		"bad when": fixtures.Build(func(row *types.ProcStep) {
			row.Tool = textEcho
			row.When = "size("
		}),
	}
}

// TestServicesProcedure_FinallyAndFail tests the teardown steps and the assertion step.
//
// Why this test is important:
//   - a procedure that sets infrastructure up (a scratch database, a kubeconfig,
//     detached containers) must tear it down after a failure and after Ctrl+C too, or
//     the operator pays for what an interrupted run left behind; and a refused input
//     must be one failed row, not a Go step written to say so.
//
// What it tests:
//   - Finally steps run after a failed step and see failed=true; after a cancelled run
//     they still run (on a detached context) and see canceled=true; a Finally step's
//     error is its failed row and never stops the next one; a fail step under when is a
//     failed row with its expanded message that stops the procedure, and an advisory
//     one is a warning; a capture that cannot decode the output is the step's failed
//     row, not an engine error.
func TestServicesProcedure_FinallyAndFail(t *testing.T) {
	t.Parallel()

	procs := finallyAndFailProcedures()
	rec := fixtures.BuildRef(func(row *recorder) {
		row.out = map[string]string{textFalse: textBoomFailure, "cat": textNotStructured}
	})
	eng := procEngine(t, rec, procs, new(procedure.Deps))

	res, err := eng.Run(
		t.Context(),
		textDB,
		map[string]any{textName: textOk, textSoft: true},
		stdx.Zero[types.RunOptions](),
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"docker compose up", textFalse, "docker compose down true"},
		rec.calls)

	st := statuses(res)
	assert.Equal(t, types.StatusSkip, st[textName])

	testServicesProcedureFinallyAndFailStatuses(t, rec, eng, st)
}

// failWhenStep returns a step that fails with message when the condition holds.
func failWhenStep(name, when, message string) types.ProcStep {
	return fixtures.Build(func(row *types.ProcStep) {
		row.Name = name
		row.When = when
		row.Fail = message
	})
}

// finallyAndFailProcedures returns the procedures TestServicesProcedure_FinallyAndFail
// runs.
func finallyAndFailProcedures() []types.Procedure {
	soft := failWhenStep(textSoft, textSoft, "soft {name}")

	soft.Advisory = true

	parse := toolStep(textRead, "cat")

	parse.Capture = &types.Capture{Var: textV, As: types.CaptureJSON}

	return []types.Procedure{
		fixtures.Build(func(row *types.Procedure) {
			row.Name = textDB
			row.Inputs = []string{textName, textSoft}
			row.Steps = []types.ProcStep{
				failWhenStep(textName, "name.contains(' ')", "name {name} has a space"),
				soft,
				toolStep(textUp, textDocker, textCompose, textUp),
				toolStep("work", textFalse),
				toolStep(textNever, textEcho),
			}
			row.Finally = []types.ProcStep{
				failWhenStep(textInterrupted, "canceled", textInterrupted),
				toolStep(textBroken, textEcho, "{missing}"),
				toolStep(textDown, textDocker, textCompose, textDown, "${failed}"),
			}
		}),
		fixtures.Build(func(row *types.Procedure) {
			row.Name = "parse"
			row.Steps = []types.ProcStep{parse}
		}),
	}
}

// testServicesProcedureFinallyAndFailStatuses continues
// TestServicesProcedure_FinallyAndFail: statuses.
func testServicesProcedureFinallyAndFailStatuses(
	t *testing.T,
	rec *recorder,
	eng interfaces.ProcedureRunner,
	statusByName map[string]types.StepStatus,
) {
	t.Helper()

	var err error

	var res types.StepResults

	assert.Equal(t, types.StatusWarn, statusByName[textSoft])
	assert.Equal(t, types.StatusFail, statusByName["work"])
	assert.NotContains(t, statusByName, textNever)
	assert.Equal(t, types.StatusSkip, statusByName[textInterrupted])
	assert.Equal(t, types.StatusFail, statusByName[textBroken])
	assert.Equal(t, types.StatusPass, statusByName[textDown])

	rec.calls = nil
	res, err = eng.Run(
		t.Context(),
		textDB,
		map[string]any{textName: "a b", textSoft: false},
		stdx.Zero[types.RunOptions](),
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"docker compose down true"}, rec.calls)
	assert.Equal(t, types.StatusFail, res[0].Status)
	assert.Equal(t, "name a b has a space", res[0].Error)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	rec.calls = nil
	res, err = eng.Run(ctx, textDB, map[string]any{textName: textOk, textSoft: false},
		stdx.Zero[types.RunOptions]())
	require.NoError(t, err)
	assert.Contains(t, rec.calls, "docker compose down true")

	statusByName = statuses(res)
	assert.Equal(t, types.StatusFail, statusByName[textInterrupted])
	assert.Equal(t, textInterrupted, res[len(res)-3].Error)

	res, err = eng.Run(t.Context(), "parse", nil, stdx.Zero[types.RunOptions]())
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.Equal(t, types.StatusFail, res[0].Status)
	assert.Contains(t, res[0].Error, "capture json")
	assert.Equal(t, textNotStructured, string(res[0].Detail))
}

// TestServicesProcedure_StepPanicIsAFailedRow tests that a Go step's panic is recovered.
//
// Why this test is important:
//   - A Go step is the residue a row cannot express (a fault injection, a credential
//     resolve); a panic in one must not unwind the run past its finally steps — the
//     cleanup that reverts a fault or removes a temporary file — nor crash the CLI or
//     the CronJob running it.
//
// What it tests:
//   - A step that panics is a failed row whose error is the coded INTERNAL "panic
//     recovered" message (no panic detail), the steps after it do not run, the finally
//     steps still do, and Run returns no error.
func TestServicesProcedure_StepPanicIsAFailedRow(t *testing.T) {
	t.Parallel()

	procs := []types.Procedure{
		fixtures.Build(func(row *types.Procedure) {
			row.Name = textP
			row.Steps = []types.ProcStep{
				fixtures.Build(func(row *types.ProcStep) {
					row.Name = textBoom
					row.Step = textBoom
				}),
				fixtures.Build(func(row *types.ProcStep) {
					row.Name = textAfter
					row.Tool = textEcho
					row.Args = []string{textAfter}
				}),
			}
			row.Finally = []types.ProcStep{
				fixtures.Build(func(row *types.ProcStep) {
					row.Name = "cleanup"
					row.Tool = textEcho
					row.Args = []string{"cleanup"}
				}),
			}
		}),
	}
	rec := new(recorder)
	eng := procEngine(t, rec, procs, fixtures.BuildRef(func(row *procedure.Deps) {
		row.Steps = map[string]procedure.StepFunc{
			textBoom: func(
				context.Context, procedure.Vars, types.RunOptions,
			) (types.StepResults, procedure.Vars, error) {
				panic("step bug")
			},
		}
	}))

	res, err := eng.Run(t.Context(), textP, nil, stdx.Zero[types.RunOptions]())

	require.NoError(t, err)

	st := statuses(res)
	assert.Equal(t, types.StatusFail, st[textBoom])
	assert.Contains(t, res[0].Error, "panic recovered")
	assert.NotContains(t, res[0].Error, "step bug")
	assert.NotContains(t, st, textAfter)
	assert.Equal(t, []string{"echo cleanup"}, rec.calls)
}

// TestServicesProcedure_SuiteEntry tests procedures as suite entries.
//
// Why this test is important:
//   - a gate (preflight, check) runs procedures beside tool runs and guards, under the
//     suite's run options.
//
// What it tests:
//   - Procedures returns one entry per procedure under the verb, grouped by the
//     procedure's group, and its job runs the procedure with the suite's options.
func TestServicesProcedure_SuiteEntry(t *testing.T) {
	t.Parallel()

	procs := []types.Procedure{fixtures.Build(func(row *types.Procedure) {
		row.Name = textP
		row.Group = "Clean"
		row.Steps = []types.ProcStep{
			fixtures.Build(func(row *types.ProcStep) {
				row.Name = textRm
				row.Tool = textRm
				row.Mutating = true
			}),
		}
	})}
	rec := new(recorder)
	entrys := suite.Procedures(
		procEngine(t, rec, procs, new(procedure.Deps)),
		procs,
		textDev,
		nil,
	)
	require.Len(t, entrys, 1)
	assert.Equal(t, "Clean", entrys[0].Group)
	assert.Equal(t, suite.KindProcedure, entrys[0].Kind)

	res, err := entrys[0].Job(suite.Env{
		Decs: fixtures.Decorations(),
		Opts: fixtures.Build(func(row *types.RunOptions) {
			row.DryRun = true
		}),
		Snapshot:  nil,
		Component: "",
	}).Execute(t.Context(), nil, "")
	require.NoError(t, err)
	assert.Empty(t, rec.calls)
	assert.Equal(t, "dry-run: rm", res[0].Error)
}

// TestServicesProcedure_EnvAndStream tests a tool step's environment and streamed output.
//
// Why this test is important:
//   - a job's environment is often a list a Go step or a CEL expression computes (the
//     database defaults, an optional override), so an env entry must expand like an
//     argument: a `{name...}` splat and a `${expr}` list, not only a placeholder; and a
//     long job (a seed, a test run) must stream its output to the operator instead of
//     buffering it until it ends.
//
// What it tests:
//   - env entries splat a list input, keep a literal, and append a `${expr}` list (empty
//     when the expression yields none); a `stream: true` step runs through RunWithEnv
//     with its env, passes when the command does and fails when it does not; a streamed
//     step with an exit capture never fails (a non-zero status is a warning naming it)
//     and sets the status for a later condition, and a Go step's `sets` declares the
//     variables a condition may read; a stream step that captures its text is refused
//     when the engine is built.
func TestServicesProcedure_EnvAndStream(t *testing.T) {
	t.Parallel()

	rec := fixtures.BuildRef(func(row *recorder) {
		row.out = map[string]string{textUvFail: textBang}
	})
	procs := testServicesProcedureEnvAndStreamTable()
	eng := procEngine(t, rec, procs, fixtures.BuildRef(func(row *procedure.Deps) {
		row.Steps = map[string]procedure.StepFunc{
			textMark: func(context.Context, procedure.Vars, types.RunOptions) (
				types.StepResults, procedure.Vars, error,
			) {
				return nil, procedure.Vars{"marked": true}, nil
			},
		}
	}))
	statusByName := map[string]any{
		textExtra: []string{textDBHostLocalhost, textDBPort5432}, textProfile: "small",
	}
	res, err := eng.Run(t.Context(), textRun, statusByName, stdx.Zero[types.RunOptions]())
	require.NoError(t, err)
	assert.Equal(
		t,
		[]string{textDBHostLocalhost, textDBPort5432, textSearchEnvDev, "PROFILE=small"},
		rec.envs["go run ./job"],
	)
	assert.Equal(
		t,
		[]string{"uv run", textUvFail, "uv run again", textUvFail},
		rec.streamed,
	)
	assert.Equal(t, []string{"A=small"}, rec.envs["uv run"])

	statusOf := statuses(res)
	assert.Equal(t, types.StatusPass, statusOf["streamed"])
	assert.Equal(t, types.StatusFail, statusOf[textBroken])
	assert.Equal(t, types.StatusWarn, statusOf[textProbe], "an exit capture never fails")

	testServicesProcedureEnvAndStreamChecksCalls(t, rec, eng, statusByName, statusOf)
}

// testServicesProcedureEnvAndStreamChecksCalls continues
// TestServicesProcedure_EnvAndStream.
func testServicesProcedureEnvAndStreamChecksCalls(
	t *testing.T,
	rec *recorder,
	eng interfaces.ProcedureRunner,
	statusByName map[string]any,
	statusOf map[string]types.StepStatus,
) {
	t.Helper()

	assert.Equal(
		t,
		types.StatusPass,
		statusOf[textAfter],
		"the condition read the exit and set",
	)

	statusByName[textProfile] = ""

	_, err := eng.Run(t.Context(), textRun, statusByName, stdx.Zero[types.RunOptions]())
	require.NoError(t, err)
	assert.Equal(t, []string{textDBHostLocalhost, textDBPort5432, textSearchEnvDev},
		rec.envs["go run ./job"])

	_, err = procedure.NewFromConfig(
		procedure.Config{
			Procedures: []types.Procedure{fixtures.Build(func(row *types.Procedure) {
				row.Name = textX
				row.Steps = []types.ProcStep{
					fixtures.Build(func(row *types.ProcStep) {
						row.Name = textS
						row.Tool = textGoLower
						row.Stream = true
						row.Capture = &types.Capture{Var: textV, As: ""}
					}),
				}
			})},
		},
		fixtures.BuildRef(func(row *procedure.Deps) {
			row.Tools = func(string) (interfaces.ToolSpec, bool) {
				return toolOf(gomock.NewController(t), textGoLower), true
			}
		}),
	)
	require.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.Contains(t, err.Error(), "stream")
}

// testServicesProcedureEnvAndStreamTable builds the table
// TestServicesProcedure_EnvAndStream runs.
func testServicesProcedureEnvAndStreamTable() []types.Procedure {
	return []types.Procedure{fixtures.Build(func(row *types.Procedure) {
		row.Name = textRun
		row.Inputs = []string{textExtra, textProfile}
		row.Steps = []types.ProcStep{
			fixtures.Build(func(row *types.ProcStep) {
				row.Name = "buffered"
				row.Tool = textGoLower
				row.Args = []string{textRun, "./job"}
				row.Env = []string{
					"{extra...}",
					textSearchEnvDev,
					"${profile != '' ? ['PROFILE=' + profile] : []}",
				}
			}),
			fixtures.Build(func(row *types.ProcStep) {
				row.Name = "streamed"
				row.Tool = textUv
				row.Args = []string{textRun}
				row.Stream = true
				row.Env = []string{
					"A={profile}",
				}
			}),
			fixtures.Build(func(row *types.ProcStep) {
				row.Name = textProbe
				row.Tool = textUv
				row.Args = []string{"fail"}
				row.Stream = true
				row.Capture = &types.Capture{Var: "probe_exit", As: types.CaptureExit}
			}),
			fixtures.Build(func(row *types.ProcStep) {
				row.Name = textMark
				row.Step = textMark
				row.Sets = []string{"marked"}
			}),
			fixtures.Build(func(row *types.ProcStep) {
				row.Name = textAfter
				row.Tool = textUv
				row.Args = []string{textRun, "again"}
				row.Stream = true
				row.When = "probe_exit == 1 && marked"
			}),
			fixtures.Build(func(row *types.ProcStep) {
				row.Name = textBroken
				row.Tool = textUv
				row.Args = []string{"fail"}
				row.Stream = true
			}),
		}
	})}
}

// TestServicesProcedure_VarsAccessors tests the typed reads a Go step makes of its
// variables.
//
// Why this test is important:
//   - A step reads its inputs through Vars: a flag the CLI typed, the list a tool call
//     sends JSON-decoded (a list of any), the exit status a capture stored. Each accessor
//     must read both forms and treat a missing or mistyped value as the zero value, so a
//     step never panics on the shape its caller happened to send.
//
// What it tests:
//   - Text, Bool, Strings, Int and Duration return the typed value, the JSON-decoded
//     forms ([]any, float64, a duration's text) converted, and the zero value for a
//     missing, mistyped or unparseable key.
func TestServicesProcedure_VarsAccessors(t *testing.T) {
	t.Parallel()

	vars := procedure.Vars{
		textEnv:     textStaging,
		textYes:     true,
		textOnly:    []string{textAPI},
		"units":     []any{textA, units},
		"code":      code,
		"exit":      int64(int64Accessors),
		textN:       float64(textNAccessors),
		"odd":       struct{}{},
		textWait:    textWaitAccessors * time.Second,
		"wait_text": "1m30s",
		"wait_bad":  "soon",
	}
	assert.Equal(t, textStaging, vars.Text(textEnv))
	assert.Empty(t, vars.Text(textYes), "a non-string reads as empty")
	assert.Empty(t, vars.Text(textMissing))
	assert.True(t, vars.Bool(textYes))
	assert.False(t, vars.Bool(textEnv), "a non-bool reads as false")
	assert.Equal(t, []string{textAPI}, vars.Strings(textOnly))
	assert.Equal(
		t,
		[]string{textA, "2"},
		vars.Strings("units"),
		"a JSON list is converted",
	)
	assert.Nil(t, vars.Strings(textEnv))
	assert.Equal(t, wantIntAccessors, vars.Int("code"))
	assert.Equal(t, wantIntVarsAccessors, vars.Int("exit"))
	assert.Equal(t, wantIntProcedureVarsAccessors, vars.Int(textN))
	assert.Zero(t, vars.Int("odd"))
	assert.Equal(t, 3*time.Second, vars.Duration(textWait))
	assert.Equal(
		t,
		90*time.Second,
		vars.Duration("wait_text"),
		"a duration's text parses",
	)
	assert.Zero(t, vars.Duration("wait_bad"))
	assert.Zero(t, vars.Duration(textEnv))
	assert.Zero(t, vars.Duration(textMissing))
}

// TestServicesProcedure_ConcurrentEachOverObjects tests a concurrent each over object
// items with path placeholders.
//
// Why this test is important:
//   - A fan-out over a run-time list of records (build targets, modules) is the residue
//     procedures most often left to Go: concurrent runs every item even when one fails
//     and keeps the items' order, and `{item.key}` paths let a row name, cwd and
//     arguments read the record, so such a fan-out is rows.
//
// What it tests:
//   - Three object items run their tool concurrently, the failing one included; the
//     results are in item order under names, cwds and args read through `{t.key}` and a
//     `{t.list...}` splat; a bad path and a non-list each are rejected; concurrent
//     without each fails compilation.
func TestServicesProcedure_ConcurrentEachOverObjects(t *testing.T) {
	t.Parallel()

	rec := fixtures.BuildRef(func(row *recorder) {
		row.out = map[string]string{"go build -o bin/b .": textBoomFailure}
	})
	targets := []any{
		map[string]any{
			textName:  textA,
			textDir:   "apps/a",
			textBin:   "bin/a",
			textExtra: []string{"-race"},
		},
		map[string]any{
			textName:  textB,
			textDir:   "apps/b",
			textBin:   "bin/b",
			textExtra: []string{},
		},
		map[string]any{
			textName:  textC,
			textDir:   "apps/c",
			textBin:   "bin/c",
			textExtra: []string{},
		},
	}
	eng := procEngine(t, rec, []types.Procedure{
		fixtures.Build(func(row *types.Procedure) {
			row.Name = textMain
			row.Inputs = []string{textTargets}
			row.Steps = []types.ProcStep{
				fixtures.Build(func(row *types.ProcStep) {
					row.Name = "{t.name}: build"
					row.Each = textTargets
					row.As = textT
					row.Concurrent = true
					row.Tool = textGoLower
					row.Cwd = "{t.dir}"
					row.Args = []string{textBuild, "{t.extra...}", "-o", "{t.bin}", "."}
				}),
				fixtures.Build(func(row *types.ProcStep) {
					row.Name = textNever
					row.Tool = textEcho
				}),
			}
		}),
		fixtures.Build(func(row *types.Procedure) {
			row.Name = "badpath"
			row.Inputs = []string{textTargets}
			row.Steps = []types.ProcStep{
				fixtures.Build(func(row *types.ProcStep) {
					row.Name = "{t.nope}"
					row.Each = textTargets
					row.As = textT
					row.Tool = textEcho
				}),
			}
		}),
	}, new(procedure.Deps))

	testServicesProcedureConcurrentEachOverObjectsChecksResults(t, rec, targets, eng)
}

// testServicesProcedureConcurrentEachOverObjectsChecksResults continues
// TestServicesProcedure_ConcurrentEachOverObjects: checks results.
func testServicesProcedureConcurrentEachOverObjectsChecksResults(
	t *testing.T,
	rec *recorder,
	targets []any,
	eng interfaces.ProcedureRunner,
) {
	t.Helper()

	res, err := eng.Run(t.Context(), textMain, map[string]any{textTargets: targets},
		stdx.Zero[types.RunOptions]())
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{
		"go build -race -o bin/a .", "go build -o bin/b .", "go build -o bin/c .",
	}, rec.calls, "every item runs, the failing one's siblings included")
	require.Len(t, res, wantResLen, "no step after the failing fan-out")
	assert.Equal(t, []string{"a: build", "b: build", "c: build"},
		[]string{res[0].Name, res[1].Name, res[2].Name}, "results keep item order")
	assert.Equal(t, types.StatusFail, res[1].Status)
	assert.Equal(t, types.StatusPass, res[2].Status)

	_, err = eng.Run(t.Context(), "badpath", map[string]any{textTargets: targets},
		stdx.Zero[types.RunOptions]())
	require.Error(t, err)
	assert.Contains(t, err.Error(), `no variable "t.nope"`)

	_, err = procedure.NewFromConfig(procedure.Config{Procedures: []types.Procedure{
		fixtures.Build(func(row *types.Procedure) {
			row.Name = textX
			row.Steps = []types.ProcStep{
				fixtures.Build(func(row *types.ProcStep) {
					row.Name = textX
					row.Tool = textEcho
					row.Concurrent = true
				}),
			}
		}),
	}}, fixtures.BuildRef(func(row *procedure.Deps) {
		row.Tools = func(string) (interfaces.ToolSpec, bool) {
			return toolOf(gomock.NewController(t), textEcho), true
		}
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "concurrent needs each")
}

// TestServicesProcedure_PlatformVariables tests the built-in os and arch variables.
//
// Why this test is important:
//   - A platform-specific row (a Windows ARM64 toolchain workaround) must be a `when:`
//     condition over the platform the engine reports, not a Go step, and a test must be
//     able to pin that platform by passing the variables as inputs.
//
// What it tests:
//   - A run sees `os` and `arch` as runtime.GOOS / GOARCH; an input of the same name
//     overrides them, so the Windows ARM64 row runs under the pinned platform and is
//     skipped under the real one.
func TestServicesProcedure_PlatformVariables(t *testing.T) {
	t.Parallel()

	rec := new(recorder)
	eng := procEngine(t, rec, []types.Procedure{
		fixtures.Build(func(row *types.Procedure) {
			row.Name = textMain
			row.Vars = []types.ProcVar{
				{Name: "winarm", Expr: "os == 'windows' && arch == 'arm64'"},
			}
			row.Steps = []types.ProcStep{
				fixtures.Build(func(row *types.ProcStep) {
					row.Name = "platform"
					row.Tool = textEcho
					row.Args = []string{"{os}/{arch}"}
				}),
				fixtures.Build(func(row *types.ProcStep) {
					row.Name = "x86 python"
					row.Tool = textEcho
					row.Args = []string{"x86"}
					row.When = "winarm"
				}),
			}
		}),
	}, new(procedure.Deps))

	res, err := eng.Run(t.Context(), textMain, nil, stdx.Zero[types.RunOptions]())
	require.NoError(t, err)
	assert.Equal(t, "echo "+runtime.GOOS+textSlash+runtime.GOARCH, rec.calls[0])
	assert.Equal(
		t,
		types.StatusSkip,
		res[1].Status,
		"the real platform is not Windows ARM64",
	)

	rec.calls = nil
	res, err = eng.Run(t.Context(), textMain,
		map[string]any{"os": textWindows, "arch": "arm64"}, stdx.Zero[types.RunOptions]())
	require.NoError(t, err)
	assert.Equal(t, []string{"echo windows/arm64", "echo x86"}, rec.calls)
	assert.Equal(t, types.StatusPass, res[1].Status)
}

// TestServicesProcedure_ExecRunsTheCallersArgv tests the exec step.
//
// Why this test is important:
//   - A command that runs whatever the operator typed (`search exec -- <cmd>`) has no
//     toolchain tool to name; without an exec step that flow stays Go. The argv must run
//     verbatim with the step's env (including removals), honour the missing policy, and
//     refuse an empty command rather than run nothing as success.
//
// What it tests:
//   - `exec: ["{command...}"]` runs the splatted argv's first element with the rest as
//     its arguments and the expanded env; an absent executable is a SKIP under
//     `missing: skip` and a FAIL naming it otherwise; an empty argv is CodeInvalidInput;
//     a step setting both tool and exec is refused at compile.
func TestServicesProcedure_ExecRunsTheCallersArgv(t *testing.T) {
	t.Parallel()

	rec := new(recorder)
	procs := testServicesProcedureExecRunsTheCallersArgvTable()
	eng := procEngine(t, rec, procs, new(procedure.Deps))

	res, err := eng.Run(
		t.Context(),
		textRun,
		map[string]any{
			textCommand: []string{textKubectl, textGet, textPods},
		},
		stdx.Zero[types.RunOptions](),
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"kubectl get pods"}, rec.streamed)
	assert.Equal(t, []string{"KUBECONFIG=/tmp/k", "-AWS_PROFILE"},
		rec.envs["kubectl get pods"])
	assert.Equal(t, types.StatusPass, res[0].Status)

	res, err = eng.Run(t.Context(), textAbsent, nil, stdx.Zero[types.RunOptions]())
	require.NoError(t, err)
	assert.Equal(t, types.StatusSkip, res[0].Status)
	assert.Equal(t, "absent not installed", res[0].Error)
	assert.Equal(t, types.StatusFail, res[1].Status)
	assert.Contains(t, res[1].Error, "absent not installed")

	_, err = eng.Run(t.Context(), textRun,
		map[string]any{textCommand: []string{}}, stdx.Zero[types.RunOptions]())
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	_, err = procedure.NewFromConfig(procedure.Config{Procedures: []types.Procedure{
		fixtures.Build(func(row *types.Procedure) {
			row.Name = textX
			row.Steps = []types.ProcStep{
				fixtures.Build(func(row *types.ProcStep) {
					row.Name = textS
					row.Tool = textEcho
					row.Exec = []string{"ls"}
				}),
			}
		}),
	}}, fixtures.BuildRef(func(row *procedure.Deps) {
		row.Tools = func(string) (interfaces.ToolSpec, bool) {
			return toolOf(gomock.NewController(t), textEcho), true
		}
	}))
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// testServicesProcedureExecRunsTheCallersArgvTable builds the table
// TestServicesProcedure_ExecRunsTheCallersArgv runs.
func testServicesProcedureExecRunsTheCallersArgvTable() []types.Procedure {
	return []types.Procedure{fixtures.Build(func(row *types.Procedure) {
		row.Name = textRun
		row.Inputs = []string{textCommand}
		row.Steps = []types.ProcStep{fixtures.Build(func(row *types.ProcStep) {
			row.Name = "child"
			row.Exec = []string{"{command...}"}
			row.Stream = true
			row.Env = []string{"KUBECONFIG=/tmp/k", "-AWS_PROFILE"}
		})}
	}), fixtures.Build(func(row *types.Procedure) {
		row.Name = textAbsent
		row.Steps = []types.ProcStep{
			fixtures.Build(func(row *types.ProcStep) {
				row.Name = "skipped"
				row.Exec = []string{textAbsent, textX}
				row.Missing = textSkip
			}),
			fixtures.Build(func(row *types.ProcStep) {
				row.Name = textFailed
				row.Exec = []string{textAbsent, textX}
			}),
		}
	})}
}

const (
	// code is the value for "code" in TestServicesProcedure_VarsAccessors.
	code = 3

	// int64Accessors is argument 1 of int64 in TestServicesProcedure_VarsAccessors.
	int64Accessors = 4

	// textNAccessors is argument 1 of float64 in TestServicesProcedure_VarsAccessors.
	textNAccessors = 5

	// textWaitAccessors is the factor multiplying time.Second in
	// TestServicesProcedure_VarsAccessors.
	textWaitAccessors = 3

	// units is an element of a []any in TestServicesProcedure_VarsAccessors.
	units = 2

	// wantIntAccessors is the expected int accessors.
	wantIntAccessors = 3

	// wantIntProcedureVarsAccessors is the expected int procedure vars accessors.
	wantIntProcedureVarsAccessors = 5

	// wantIntVarsAccessors is the expected int vars accessors.
	wantIntVarsAccessors = 4
)

const (
	// textBoomFailure is the literal "!boom".
	textBoomFailure = "!boom"

	// textDBHostLocalhost is the literal "DB_HOST=localhost".
	textDBHostLocalhost = "DB_HOST=localhost"

	// textDockerImagesFilter is the literal shown below.
	textDockerImagesFilter = "docker images --filter reference=app-* --format {{.ID}}"

	// textFalse is the literal "false".
	textFalse = "false"

	// textInterrupted is the literal "interrupted".
	textInterrupted = "interrupted"

	// textMark is the literal "mark".
	textMark = "mark"

	// textPrune is the literal "prune".
	textPrune = "prune"

	// textSearchEnvDev is the literal "SEARCH_ENV=dev".
	textSearchEnvDev = "SEARCH_ENV=dev"

	// textSkip is the literal "skip".
	textSkip = "skip"

	// textSoft is the literal "soft".
	textSoft = "soft"

	// textTargets is the literal "targets".
	textTargets = "targets"

	// textUvFail is the literal "uv fail".
	textUvFail = "uv fail"

	// textYes is the literal "yes".
	textYes = "yes"

	// textBang is the literal "!".
	textBang = "!"

	// textDBPort5432 is the literal "DB_PORT=5432".
	textDBPort5432 = "DB_PORT=5432"
)
