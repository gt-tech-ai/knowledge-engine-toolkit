package unit_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.yaml.in/yaml/v3"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
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

	// calls are the command lines run, in order.
	calls []string

	// mu guards calls.
	mu sync.Mutex
}

// runner returns the mock.
func (r *recorder) runner(ctrl *gomock.Controller) *mocks.MockCommandRunner {
	m := mocks.NewMockCommandRunner(ctrl)
	m.EXPECT().
		Exists(gomock.Any()).
		DoAndReturn(func(n string) bool { return n != "absent" }).
		AnyTimes()
	m.EXPECT().RunBufferedWithEnv(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
		gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, _ []string, exe string,
			args ...string,
		) interfaces.CmdResult {
			line := strings.Join(append([]string{exe}, args...), " ")
			r.mu.Lock()
			r.calls = append(r.calls, line)
			r.mu.Unlock()
			out := r.out[line]
			if rest, failed := strings.CutPrefix(out, "!"); failed {
				return interfaces.CmdResult{
					Err:    apperr.Sentinel("exit 1"),
					Stderr: []byte(rest),
				}
			}
			return interfaces.CmdResult{Stdout: []byte(out)}
		}).AnyTimes()
	return m
}

// procEngine builds procs over rec with every tool run directly by its name.
func procEngine(
	t *testing.T,
	rec *recorder,
	procs []types.Procedure,
	deps procedure.Deps,
) interfaces.ProcedureRunner {
	t.Helper()
	ctrl := gomock.NewController(t)
	deps.Runner = rec.runner(ctrl)
	deps.Tools = func(name string) (interfaces.ToolSpec, bool) {
		return toolOf(ctrl, name), name != "unknown"
	}
	eng, err := procedure.NewFromConfig(procedure.Config{Procedures: procs}, deps)
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
		"project": "app", "compose_files": []string{"-f", "c.yaml"},
		"remove_all": false,
	}
	listings := map[string]string{
		"docker images --filter reference=app-* --format {{.ID}}": "a1\nb2\n",
	}
	rec := &recorder{out: listings}
	ctrl := gomock.NewController(t)
	prompter := mocks.NewMockPrompter(ctrl)
	prompter.EXPECT().Confirm(gomock.Any(),
		"Remove every app container, image and volume?").Return(true, nil)
	eng := procEngine(t, rec, []types.Procedure{pruneProcedure(t)},
		procedure.Deps{Prompter: prompter})

	res, err := eng.Run(t.Context(), "prune", in, types.RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"docker compose -f c.yaml --profile gateway --profile observability down " +
			"--volumes --remove-orphans --rmi local",
		"docker images --filter reference=app-* --format {{.ID}}",
		"docker rmi -f a1 b2",
		"docker image prune -f",
		"docker volume ls -q --filter name=app",
	}, rec.calls)
	assert.Equal(t, types.StatusSkip, statuses(res)["remove volumes"])
	assert.Equal(t, "prune", res[0].Group)

	dry := &recorder{out: listings}
	eng = procEngine(t, dry, []types.Procedure{pruneProcedure(t)}, procedure.Deps{})
	res, err = eng.Run(t.Context(), "prune", in, types.RunOptions{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"docker images --filter reference=app-* --format {{.ID}}",
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
//     variables reach later steps; json and exit captures; an advisory failure continues
//     and a plain failure stops; a missing tool with missing: skip is skipped.
func TestServicesProcedure_ControlFlow(t *testing.T) {
	t.Parallel()
	rec := &recorder{out: map[string]string{
		"kubectl get -o json": `{"items":["x"]}`, "false": "!boom", "probe": "!",
	}}
	setter := func(_ context.Context, vars procedure.Vars, _ types.RunOptions,
	) (types.StepResults, procedure.Vars, error) {
		return types.StepResults{{Name: "go", Status: types.StatusPass}},
			procedure.Vars{"tag": vars["env"].(string) + "-1"}, nil
	}
	eng := procEngine(t, rec, []types.Procedure{
		{Name: "main", Inputs: []string{"env", "svcs"}, Steps: []types.ProcStep{
			{
				Name: "deploy {svc}", Each: "svcs", As: "svc", Tool: "helm",
				Args: []string{"upgrade", "{svc}", "--set", "env={env}"},
			},
			{Name: "fan", Parallel: []types.ProcStep{
				{
					Name: "pods", Tool: "kubectl", Args: []string{"get", "-o", "json"},
					Capture: &types.Capture{Var: "pods", As: types.CaptureJSON},
				},
				{Name: "probe", Tool: "probe", Capture: &types.Capture{
					Var: "ok",
					As:  types.CaptureExit,
				}},
			}},
			{Name: "set", Step: "set"},
			{Name: "sub", Call: "sub"},
			{Name: "count", Tool: "echo", Args: []string{"${size(pods.items)}", "${ok}"}},
			{Name: "absent", Tool: "absent", Missing: "skip"},
			{Name: "soft", Tool: "false", Advisory: true},
			{Name: "hard", Tool: "false"},
			{Name: "never", Tool: "echo"},
		}},
		{Name: "sub", Inputs: []string{"tag"}, Steps: []types.ProcStep{
			{Name: "tag", Tool: "docker", Args: []string{"tag", "{tag}"}},
		}},
	}, procedure.Deps{Steps: map[string]procedure.StepFunc{"set": setter}})

	res, err := eng.Run(t.Context(), "main",
		map[string]any{"env": "dev", "svcs": []string{"api", "web"}}, types.RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"helm upgrade api --set env=dev", "helm upgrade web --set env=dev",
	}, rec.calls[:2])
	assert.ElementsMatch(t, []string{"kubectl get -o json", "probe"}, rec.calls[2:4])
	assert.Equal(
		t,
		[]string{"docker tag dev-1", "echo 1 1", "false", "false"},
		rec.calls[4:],
	)
	st := statuses(res)
	assert.Equal(t, types.StatusPass, st["deploy web"])
	assert.Equal(t, types.StatusSkip, st["absent"])
	assert.Equal(t, types.StatusFail, st["soft"])
	assert.Equal(t, types.StatusFail, st["hard"])
	assert.NotContains(t, st, "never")
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
	confirmed := []types.Procedure{{Name: "p", Steps: []types.ProcStep{
		{Name: "rm", Confirm: "Delete?", Tool: "rm"},
	}}}
	rec := &recorder{}
	eng := procEngine(t, rec, confirmed, procedure.Deps{})
	_, err := eng.Run(t.Context(), "p", nil, types.RunOptions{})
	assert.Equal(t, apperr.CodeCanceled, apperr.Code(err))
	assert.Empty(t, rec.calls)
	_, err = eng.Run(t.Context(), "p", nil, types.RunOptions{Yes: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"rm"}, rec.calls)

	_, err = eng.Run(t.Context(), "nope", nil, types.RunOptions{})
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))
	eng = procEngine(t, rec, []types.Procedure{{Name: "v", Steps: []types.ProcStep{
		{Name: "x", Tool: "echo", Args: []string{"{missing}"}},
	}}}, procedure.Deps{})
	_, err = eng.Run(t.Context(), "v", nil, types.RunOptions{})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	for name, st := range map[string]types.ProcStep{
		"two kinds":    {Tool: "echo", Call: "p"},
		"no kind":      {},
		"unknown tool": {Tool: "unknown"},
		"unknown step": {Step: "nope"},
		"unknown call": {Call: "nope"},
		"bad capture":  {Tool: "echo", Capture: &types.Capture{Var: "v", As: "xml"}},
		"bad when":     {Tool: "echo", When: "size("},
	} {
		_, err := procedure.NewFromConfig(procedure.Config{Procedures: []types.Procedure{
			{Name: "p", Steps: []types.ProcStep{st}},
		}}, procedure.Deps{Tools: func(n string) (interfaces.ToolSpec, bool) {
			return nil, n != "unknown"
		}})
		require.Error(t, err, name)
	}
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
	procs := []types.Procedure{{Name: "p", Group: "Clean", Steps: []types.ProcStep{
		{Name: "rm", Tool: "rm", Mutating: true},
	}}}
	rec := &recorder{}
	es := suite.Procedures(procEngine(t, rec, procs, procedure.Deps{}), procs, "dev", nil)
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
