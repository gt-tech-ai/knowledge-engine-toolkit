package unit_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
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

// streamFlag names the stream option in a signature.
type streamFlag bool

// record notes one invocation and answers it from out.
func (r *recorder) record(
	env []string,
	exe string,
	args []string,
	stream streamFlag,
) (string, bool) {
	line := strings.Join(append([]string{exe}, args...), " ")

	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls = append(r.calls, line)
	if stream {
		r.streamed = append(r.streamed, line)
	}

	if r.envs == nil {
		r.envs = map[string][]string{}
	}

	r.envs[line] = env

	rest, failed := strings.CutPrefix(r.out[line], textBang)

	return rest, failed
}

// runner returns the mock.
func (r *recorder) runner(ctrl *gomock.Controller) *mocks.MockCommandRunner {
	m := mocks.NewMockCommandRunner(ctrl)
	m.EXPECT().
		Exists(gomock.Any()).
		DoAndReturn(func(n string) bool { return n != textAbsent }).
		AnyTimes()
	m.EXPECT().RunBufferedWithEnv(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, env []string, exe string,
			args ...string,
		) interfaces.CmdResult {
			out, failed := r.record(env, exe, args, false)
			if failed {
				return interfaces.CmdResult{
					Err:    apperr.Sentinel("exit 1"),
					Stderr: []byte(out),
				}
			}

			return interfaces.CmdResult{Stdout: []byte(out)}
		}).AnyTimes()
	m.EXPECT().RunWithEnv(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, env []string, exe string,
			args ...string,
		) error {
			if _, failed := r.record(env, exe, args, true); failed {
				return apperr.Sentinel("exit 1")
			}

			return nil
		}).AnyTimes()

	return m
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

	in := map[string]any{
		"project": textApp, "compose_files": []string{"-f", "c.yaml"},
		"remove_all": false,
	}
	listings := map[string]string{
		textDockerImagesFilter: "a1\nb2\n",
	}
	rec := &recorder{out: listings}
	ctrl := gomock.NewController(t)
	prompter := mocks.NewMockPrompter(ctrl)
	prompter.EXPECT().Confirm(gomock.Any(),
		"Remove every app container, image and volume?").Return(true, nil)

	eng := procEngine(t, rec, []types.Procedure{pruneProcedure(t)},
		&procedure.Deps{Prompter: prompter})

	res, err := eng.Run(t.Context(), textPrune, in, types.RunOptions{})
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

	dry := &recorder{out: listings}

	eng = procEngine(t, dry, []types.Procedure{pruneProcedure(t)}, &procedure.Deps{})
	res, err = eng.Run(t.Context(), textPrune, in, types.RunOptions{DryRun: true})
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

	rec := &recorder{out: map[string]string{
		"kubectl get -o json": `{"items":["x"]}`,
		textFalse:             textBoom2,
		textProbe:             textBang,
	}}
	setter := func(_ context.Context, vars procedure.Vars, _ types.RunOptions,
	) (types.StepResults, procedure.Vars, error) {
		return types.StepResults{{Name: textGo2, Status: types.StatusPass}},
			procedure.Vars{
				textTag:  asType[string](t, vars[textEnv]) + "-1",
				"policy": textSkip,
			}, nil
	}
	eng := procEngine(
		t,
		rec,
		testServicesProcedureControlFlowTable(),
		&procedure.Deps{Steps: map[string]procedure.StepFunc{textSet: setter}},
	)

	res, err := eng.Run(
		t.Context(),
		textMain,
		map[string]any{
			textEnv:  textDev,
			textSvcs: []string{textAPI, textWeb},
		},
		types.RunOptions{},
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

	st := statuses(res)
	assert.Equal(t, types.StatusPass, st["deploy web"])
	assert.Equal(t, types.StatusSkip, st[textAbsent])
	assert.Equal(t, types.StatusSkip, st["absent-var"])
	assert.Equal(t, types.StatusWarn, st[textSoft])
	assert.Equal(t, types.StatusFail, st["hard"])
	assert.NotContains(t, st, textNever)
}

// testServicesProcedureControlFlowTable builds the table
// TestServicesProcedure_ControlFlow runs.
func testServicesProcedureControlFlowTable() []types.Procedure {
	return []types.Procedure{
		{Name: textMain, Inputs: []string{textEnv, textSvcs}, Steps: []types.ProcStep{
			{
				Name: "deploy {svc}", Each: textSvcs, As: textSvc, Tool: textHelm,
				Args: []string{"upgrade", "{svc}", "--set", "env={env}"},
			},
			{Name: "fan", Parallel: []types.ProcStep{
				{
					Name:    textPods,
					Tool:    textKubectl,
					Args:    []string{textGet, "-o", textStructuredFormat},
					Capture: &types.Capture{Var: textPods, As: types.CaptureJSON},
				},
				{Name: textProbe, Tool: textProbe, Capture: &types.Capture{
					Var: textOk,
					As:  types.CaptureExit,
				}},
			}},
			{Name: textSet, Step: textSet},
			{Name: textSub, Call: textSub},
			{
				Name: textCount,
				Tool: textEcho,
				Args: []string{"${size(pods.items)}", "${ok}"},
			},
			{Name: textAbsent, Tool: textAbsent, Missing: textSkip},
			{Name: "absent-var", Tool: textAbsent, Missing: "{policy}"},
			{Name: textSoft, Tool: textFalse, Advisory: true},
			{Name: "hard", Tool: textFalse},
			{Name: textNever, Tool: textEcho},
		}},
		{Name: textSub, Inputs: []string{textTag}, Steps: []types.ProcStep{
			{Name: textTag, Tool: textDocker, Args: []string{textTag, "{tag}"}},
		}},
	}
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

	confirmed := []types.Procedure{{Name: textP, Steps: []types.ProcStep{
		{Name: textRm, Confirm: "Delete?", Tool: textRm},
	}}}
	rec := &recorder{}
	eng := procEngine(t, rec, confirmed, &procedure.Deps{})
	_, err := eng.Run(t.Context(), textP, nil, types.RunOptions{})
	assert.Equal(t, apperr.CodeCanceled, apperr.Code(err))
	assert.Empty(t, rec.calls)

	_, err = eng.Run(t.Context(), textP, nil, types.RunOptions{Yes: true})
	require.NoError(t, err)
	assert.Equal(t, []string{textRm}, rec.calls)

	_, err = eng.Run(t.Context(), textNope, nil, types.RunOptions{})
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))

	eng = procEngine(t, rec, []types.Procedure{{Name: textV, Steps: []types.ProcStep{
		{Name: textX, Tool: textEcho, Args: []string{"{missing}"}},
	}}}, &procedure.Deps{})
	_, err = eng.Run(t.Context(), textV, nil, types.RunOptions{})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	for name, st := range map[string]types.ProcStep{
		"two kinds":    {Tool: textEcho, Call: textP},
		"fail + tool":  {Tool: textEcho, Fail: "no"},
		"no kind":      {},
		"unknown tool": {Tool: textUnknown},
		"unknown step": {Step: textNope},
		"unknown call": {Call: textNope},
		"bad capture":  {Tool: textEcho, Capture: &types.Capture{Var: textV, As: "xml"}},
		"bad when":     {Tool: textEcho, When: "size("},
	} {
		_, err := procedure.NewFromConfig(procedure.Config{Procedures: []types.Procedure{
			{Name: textP, Steps: []types.ProcStep{st}},
		}}, &procedure.Deps{Tools: func(n string) (interfaces.ToolSpec, bool) {
			return nil, n != textUnknown
		}})
		require.Error(t, err, name)
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

	procs := []types.Procedure{
		{Name: textDB, Inputs: []string{textName, textSoft}, Steps: []types.ProcStep{
			{Name: textName, When: "name.contains(' ')", Fail: "name {name} has a space"},
			{Name: textSoft, When: textSoft, Fail: "soft {name}", Advisory: true},
			{Name: textUp, Tool: textDocker, Args: []string{textCompose, textUp}},
			{Name: "work", Tool: textFalse},
			{Name: textNever, Tool: textEcho},
		}, Finally: []types.ProcStep{
			{Name: textInterrupted, When: "canceled", Fail: textInterrupted},
			{Name: textBroken, Tool: textEcho, Args: []string{"{missing}"}},
			{
				Name: textDown,
				Tool: textDocker,
				Args: []string{textCompose, textDown, "${failed}"},
			},
		}},
		{Name: "parse", Steps: []types.ProcStep{{
			Name: textRead, Tool: "cat",
			Capture: &types.Capture{Var: textV, As: types.CaptureJSON},
		}}},
	}
	rec := &recorder{
		out: map[string]string{textFalse: textBoom2, "cat": textNotStructured},
	}
	eng := procEngine(t, rec, procs, &procedure.Deps{})

	res, err := eng.Run(
		t.Context(),
		textDB,
		map[string]any{textName: textOk, textSoft: true},
		types.RunOptions{},
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"docker compose up", textFalse, "docker compose down true"},
		rec.calls)

	st := statuses(res)
	assert.Equal(t, types.StatusSkip, st[textName])

	testServicesProcedureFinallyAndFailRest(t, rec, eng, st)
}

// testServicesProcedureFinallyAndFailRest continues TestServicesProcedure_FinallyAndFail
// with its remaining steps.
func testServicesProcedureFinallyAndFailRest(
	t *testing.T,
	rec *recorder,
	eng interfaces.ProcedureRunner,
	st map[string]types.StepStatus,
) {
	t.Helper()

	var err error

	var res types.StepResults

	assert.Equal(t, types.StatusWarn, st[textSoft])
	assert.Equal(t, types.StatusFail, st["work"])
	assert.NotContains(t, st, textNever)
	assert.Equal(t, types.StatusSkip, st[textInterrupted])
	assert.Equal(t, types.StatusFail, st[textBroken])
	assert.Equal(t, types.StatusPass, st[textDown])

	rec.calls = nil
	res, err = eng.Run(
		t.Context(),
		textDB,
		map[string]any{textName: "a b", textSoft: false},
		types.RunOptions{},
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"docker compose down true"}, rec.calls)
	assert.Equal(t, types.StatusFail, res[0].Status)
	assert.Equal(t, "name a b has a space", res[0].Error)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	rec.calls = nil
	res, err = eng.Run(ctx, textDB, map[string]any{textName: textOk, textSoft: false},
		types.RunOptions{})
	require.NoError(t, err)
	assert.Contains(t, rec.calls, "docker compose down true")

	st = statuses(res)
	assert.Equal(t, types.StatusFail, st[textInterrupted])
	assert.Equal(t, textInterrupted, res[len(res)-3].Error)

	res, err = eng.Run(t.Context(), "parse", nil, types.RunOptions{})
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
		{
			Name: textP,
			Steps: []types.ProcStep{
				{Name: textBoom, Step: textBoom},
				{Name: textAfter, Tool: textEcho, Args: []string{textAfter}},
			},
			Finally: []types.ProcStep{
				{Name: "cleanup", Tool: textEcho, Args: []string{"cleanup"}},
			},
		},
	}
	rec := &recorder{}
	eng := procEngine(t, rec, procs, &procedure.Deps{Steps: map[string]procedure.StepFunc{
		textBoom: func(
			context.Context, procedure.Vars, types.RunOptions,
		) (types.StepResults, procedure.Vars, error) {
			panic("step bug")
		},
	}})

	res, err := eng.Run(t.Context(), textP, nil, types.RunOptions{})

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

	procs := []types.Procedure{{Name: textP, Group: "Clean", Steps: []types.ProcStep{
		{Name: textRm, Tool: textRm, Mutating: true},
	}}}
	rec := &recorder{}
	es := suite.Procedures(
		procEngine(t, rec, procs, &procedure.Deps{}),
		procs,
		textDev,
		nil,
	)
	require.Len(t, es, 1)
	assert.Equal(t, "Clean", es[0].Group)
	assert.Equal(t, suite.KindProcedure, es[0].Kind)

	res, err := es[0].Job(suite.Env{
		Decs: fixtures.Decorations(),
		Opts: types.RunOptions{DryRun: true},
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

	rec := &recorder{out: map[string]string{textUvFail: textBang}}
	procs := testServicesProcedureEnvAndStreamTable()
	eng := procEngine(t, rec, procs, &procedure.Deps{Steps: map[string]procedure.StepFunc{
		textMark: func(context.Context, procedure.Vars, types.RunOptions) (
			types.StepResults, procedure.Vars, error,
		) {
			return nil, procedure.Vars{"marked": true}, nil
		},
	}})
	in := map[string]any{
		textExtra: []string{textDBHostLocalhost, textDBPort5432}, textProfile: "small",
	}
	res, err := eng.Run(t.Context(), textRun, in, types.RunOptions{})
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

	st := statuses(res)
	assert.Equal(t, types.StatusPass, st["streamed"])
	assert.Equal(t, types.StatusFail, st[textBroken])
	assert.Equal(t, types.StatusWarn, st[textProbe], "an exit capture never fails")
	assert.Equal(
		t,
		types.StatusPass,
		st[textAfter],
		"the condition read the exit and set",
	)

	in[textProfile] = ""
	_, err = eng.Run(t.Context(), textRun, in, types.RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{textDBHostLocalhost, textDBPort5432, textSearchEnvDev},
		rec.envs["go run ./job"])

	_, err = procedure.NewFromConfig(procedure.Config{Procedures: []types.Procedure{{
		Name: textX, Steps: []types.ProcStep{{
			Name: textS, Tool: textGo2, Stream: true, Capture: &types.Capture{Var: textV},
		}},
	}}}, &procedure.Deps{Tools: func(string) (interfaces.ToolSpec, bool) {
		return toolOf(gomock.NewController(t), textGo2), true
	}})
	require.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.Contains(t, err.Error(), "stream")
}

// testServicesProcedureEnvAndStreamTable builds the table
// TestServicesProcedure_EnvAndStream runs.
func testServicesProcedureEnvAndStreamTable() []types.Procedure {
	return []types.Procedure{{
		Name: textRun, Inputs: []string{textExtra, textProfile},
		Steps: []types.ProcStep{
			{
				Name: "buffered",
				Tool: textGo2,
				Args: []string{textRun, "./job"},
				Env: []string{
					"{extra...}",
					textSearchEnvDev,
					"${profile != '' ? ['PROFILE=' + profile] : []}",
				},
			},
			{
				Name:   "streamed",
				Tool:   textUv,
				Args:   []string{textRun},
				Stream: true,
				Env: []string{
					"A={profile}",
				},
			},
			{
				Name: textProbe, Tool: textUv, Args: []string{"fail"}, Stream: true,
				Capture: &types.Capture{Var: "probe_exit", As: types.CaptureExit},
			},
			{Name: textMark, Step: textMark, Sets: []string{"marked"}},
			{
				Name:   textAfter,
				Tool:   textUv,
				Args:   []string{textRun, "again"},
				Stream: true,
				When:   "probe_exit == 1 && marked",
			},
			{Name: textBroken, Tool: textUv, Args: []string{"fail"}, Stream: true},
		},
	}}
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
		"units":     []any{textA, units2},
		"code":      code3,
		"exit":      int64(int64Arg4),
		textN:       float64(float64Arg5),
		"odd":       struct{}{},
		textWait:    seconds3 * time.Second,
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
	assert.Equal(t, wantInt3, vars.Int("code"))
	assert.Equal(t, wantInt4, vars.Int("exit"))
	assert.Equal(t, wantInt5, vars.Int(textN))
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

	rec := &recorder{out: map[string]string{"go build -o bin/b .": textBoom2}}
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
		{Name: textMain, Inputs: []string{textTargets}, Steps: []types.ProcStep{
			{
				Name: "{t.name}: build", Each: textTargets, As: textT, Concurrent: true,
				Tool: textGo2, Cwd: "{t.dir}",
				Args: []string{textBuild, "{t.extra...}", "-o", "{t.bin}", "."},
			},
			{Name: textNever, Tool: textEcho},
		}},
		{Name: "badpath", Inputs: []string{textTargets}, Steps: []types.ProcStep{
			{Name: "{t.nope}", Each: textTargets, As: textT, Tool: textEcho},
		}},
	}, &procedure.Deps{})

	testServicesProcedureConcurrentEachOverObjectsRest(t, rec, targets, eng)
}

// testServicesProcedureConcurrentEachOverObjectsRest continues
// TestServicesProcedure_ConcurrentEachOverObjects with its remaining steps.
func testServicesProcedureConcurrentEachOverObjectsRest(
	t *testing.T,
	rec *recorder,
	targets []any,
	eng interfaces.ProcedureRunner,
) {
	t.Helper()

	res, err := eng.Run(t.Context(), textMain, map[string]any{textTargets: targets},
		types.RunOptions{})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{
		"go build -race -o bin/a .", "go build -o bin/b .", "go build -o bin/c .",
	}, rec.calls, "every item runs, the failing one's siblings included")
	require.Len(t, res, wantResLen3, "no step after the failing fan-out")
	assert.Equal(t, []string{"a: build", "b: build", "c: build"},
		[]string{res[0].Name, res[1].Name, res[2].Name}, "results keep item order")
	assert.Equal(t, types.StatusFail, res[1].Status)
	assert.Equal(t, types.StatusPass, res[2].Status)

	_, err = eng.Run(t.Context(), "badpath", map[string]any{textTargets: targets},
		types.RunOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `no variable "t.nope"`)

	_, err = procedure.NewFromConfig(procedure.Config{Procedures: []types.Procedure{
		{
			Name:  textX,
			Steps: []types.ProcStep{{Name: textX, Tool: textEcho, Concurrent: true}},
		},
	}}, &procedure.Deps{Tools: func(string) (interfaces.ToolSpec, bool) {
		return toolOf(gomock.NewController(t), textEcho), true
	}})
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

	rec := &recorder{}
	eng := procEngine(t, rec, []types.Procedure{
		{Name: textMain, Vars: []types.ProcVar{
			{Name: "winarm", Expr: "os == 'windows' && arch == 'arm64'"},
		}, Steps: []types.ProcStep{
			{Name: "platform", Tool: textEcho, Args: []string{"{os}/{arch}"}},
			{Name: "x86 python", Tool: textEcho, Args: []string{"x86"}, When: "winarm"},
		}},
	}, &procedure.Deps{})

	res, err := eng.Run(t.Context(), textMain, nil, types.RunOptions{})
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
		map[string]any{"os": textWindows, "arch": "arm64"}, types.RunOptions{})
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

	rec := &recorder{}
	procs := []types.Procedure{{
		Name: textRun, Inputs: []string{textCommand},
		Steps: []types.ProcStep{{
			Name: "child", Exec: []string{"{command...}"}, Stream: true,
			Env: []string{"KUBECONFIG=/tmp/k", "-AWS_PROFILE"},
		}},
	}, {
		Name: textAbsent, Steps: []types.ProcStep{
			{Name: "skipped", Exec: []string{textAbsent, textX}, Missing: textSkip},
			{Name: textFailed, Exec: []string{textAbsent, textX}},
		},
	}}
	eng := procEngine(t, rec, procs, &procedure.Deps{})

	res, err := eng.Run(
		t.Context(),
		textRun,
		map[string]any{
			textCommand: []string{textKubectl, textGet, textPods},
		},
		types.RunOptions{},
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"kubectl get pods"}, rec.streamed)
	assert.Equal(t, []string{"KUBECONFIG=/tmp/k", "-AWS_PROFILE"},
		rec.envs["kubectl get pods"])
	assert.Equal(t, types.StatusPass, res[0].Status)

	res, err = eng.Run(t.Context(), textAbsent, nil, types.RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusSkip, res[0].Status)
	assert.Equal(t, "absent not installed", res[0].Error)
	assert.Equal(t, types.StatusFail, res[1].Status)
	assert.Contains(t, res[1].Error, "absent not installed")

	_, err = eng.Run(t.Context(), textRun,
		map[string]any{textCommand: []string{}}, types.RunOptions{})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	_, err = procedure.NewFromConfig(procedure.Config{Procedures: []types.Procedure{
		{
			Name:  textX,
			Steps: []types.ProcStep{{Name: textS, Tool: textEcho, Exec: []string{"ls"}}},
		},
	}}, &procedure.Deps{Tools: func(string) (interfaces.ToolSpec, bool) {
		return toolOf(gomock.NewController(t), textEcho), true
	}})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}
