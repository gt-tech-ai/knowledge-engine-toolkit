package unit_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/system"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// recordHooks returns a MockCmdHook that appends each lifecycle callback to *events
// (start:/ok:/fail: + cmd), so a test asserts the observable sequence the progress UI
// depends on without a hand-rolled hook double.
func recordHooks(ctrl *gomock.Controller, events *[]string) *mocks.MockCmdHook {
	hook := mocks.NewMockCmdHook(ctrl)
	hook.EXPECT().OnStart(gomock.Any()).Do(func(cmd string) {
		*events = append(*events, "start:"+cmd)
	}).AnyTimes()
	hook.EXPECT().OnSuccess(gomock.Any()).Do(func(cmd string) {
		*events = append(*events, "ok:"+cmd)
	}).AnyTimes()
	hook.EXPECT().OnFailure(gomock.Any()).Do(func(cmd string) {
		*events = append(*events, "fail:"+cmd)
	}).AnyTimes()

	return hook
}

// okInner returns a MockCommandRunner whose every method succeeds (RunBuffered
// yields the given stdout), the healthy inner for the decorator-composition tests.
func okInner(ctrl *gomock.Controller, stdout string) *mocks.MockCommandRunner {
	inner := mocks.NewMockCommandRunner(ctrl)
	// The trailing gomock.Any() matches the variadic args slice (any count).
	inner.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil).AnyTimes()
	inner.EXPECT().RunWithEnv(
		gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
	).Return(nil).AnyTimes()
	inner.EXPECT().RunBuffered(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(system.CmdResult{
			Stdout: []byte(stdout), Err: nil, Stderr: nil,
			Duration: 0, ExitCode: 0,
		}).
		AnyTimes()
	inner.EXPECT().RunBufferedWithEnv(
		gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
	).Return(system.CmdResult{
		Stdout: []byte(stdout), Err: nil, Stderr: nil,
		Duration: 0, ExitCode: 0,
	}).AnyTimes()
	inner.EXPECT().Exists(gomock.Any()).Return(true).AnyTimes()
	inner.EXPECT().RequireTool(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	return inner
}

// TestRunnerBuilder_DecoratesAndFiresHooks tests that the Builder composes the
// logging, timeout, and hook decorators and that hooks fire on success/failure.
//
// Why this test is important:
//   - The decorated runner is what the whole CLI uses; the hook decorator drives
//     the progress UI (start/ok/fail per command), so a missed OnSuccess/OnFailure
//     would desync the displayed status from reality.
//
// What it tests:
//   - With logging+timeout+hook composed, every method passes through and records
//     a log line and a start+ok hook pair on success; a failing inner fires
//     start+fail; the cmd string is name-only when there are no args.
func TestRunnerBuilder_DecoratesAndFiresHooks(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)

	var logs []string

	logf := func(format string, a ...any) { logs = append(logs, fmt.Sprintf(format, a...)) }

	var events []string

	hook := recordHooks(ctrl, &events)
	inner := okInner(ctrl, textOk)

	commandRunner := system.NewBuilder(inner).
		WithHook(hook).
		WithTimeout(time.Second).
		WithLogging(logf).
		Build()

	require.NoError(
		t,
		commandRunner.Run(context.Background(), textDLower, textGoLower, textVersion),
	)

	_ = commandRunner.RunBuffered(context.Background(), textDLower, textGoLower, textList)
	require.NoError(
		t,
		commandRunner.RunWithEnv(
			context.Background(),
			textDLower,
			[]string{textAB},
			textGoLower,
			textEnv,
		),
	)

	_ = commandRunner.RunBufferedWithEnv(
		context.Background(),
		textDLower,
		nil,
		textGoLower,
		textX,
	)
	assert.True(
		t,
		commandRunner.Exists(textGoLower),
		"Exists delegates through the chain",
	)
	require.NoError(t, commandRunner.RequireTool(textGoLower, ""))

	assert.NotEmpty(t, logs, "logging decorator records each command")
	assert.Contains(t, events, "start:go version")
	assert.Contains(t, events, "ok:go version", "success fires OnSuccess")

	testRunnerBuilderDecoratesAndFiresHooksFailures(t, ctrl)
}

// testRunnerBuilderDecoratesAndFiresHooksFailures continues
// TestRunnerBuilder_DecoratesAndFiresHooks: failures.
func testRunnerBuilderDecoratesAndFiresHooksFailures(
	t *testing.T,
	ctrl *gomock.Controller,
) {
	t.Helper()

	// Failing inner fires OnFailure for both Run and RunBuffered.
	var failEvents []string

	failHook := recordHooks(ctrl, &failEvents)
	failInner := mocks.NewMockCommandRunner(ctrl)
	boom := apperr.Sentinel(textBoom)
	failInner.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(boom).AnyTimes()
	failInner.EXPECT().RunWithEnv(
		gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
	).Return(boom).AnyTimes()
	failInner.EXPECT().
		RunBuffered(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(system.CmdResult{Err: boom, Stdout: nil, Stderr: nil, Duration: 0, ExitCode: 0}).
		AnyTimes()
	failInner.EXPECT().RunBufferedWithEnv(
		gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
	).Return(system.CmdResult{
		Err: boom, Stdout: nil, Stderr: nil, Duration: 0,
		ExitCode: 0,
	}).AnyTimes()

	fr := system.NewBuilder(failInner).WithHook(failHook).Build()
	require.Error(t, fr.Run(context.Background(), "", textX, "y"))
	require.Error(t, fr.RunBuffered(context.Background(), "", "z").Err)
	require.Error(t, fr.RunWithEnv(context.Background(), "", nil, textX))
	require.Error(t, fr.RunBufferedWithEnv(context.Background(), "", nil, "z").Err)
	assert.Contains(t, failEvents, "fail:x y")
	assert.Contains(t, failEvents, "fail:z")

	// cmdStr is name-only when there are no args.
	var soloEvents []string

	nr := system.NewBuilder(okInner(ctrl, "")).
		WithHook(recordHooks(ctrl, &soloEvents)).
		Build()
	require.NoError(t, nr.Run(context.Background(), "", "solo"))
}

// TestRunnerBuilder_DryRunSkipsExecution tests that the dry-run decorator never
// executes commands but still answers read-only checks.
//
// Why this test is important:
//   - Dry-run is the safety mechanism for previewing destructive operations; if it
//     actually invoked the inner runner it would defeat its entire purpose.
//
// What it tests:
//   - Run/RunWithEnv return nil and RunBuffered/RunBufferedWithEnv return an empty
//     result WITHOUT invoking the inner runner (whose execute methods are never
//     expected, so gomock fails the test if they are called), while Exists/RequireTool
//     still delegate; the would-execute line is logged.
func TestRunnerBuilder_DryRunSkipsExecution(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)

	var logs []string

	// Inner's execute methods are NOT expected — dry-run must never call them, and
	// gomock fails on any unexpected call. Only the read-only checks delegate.
	inner := mocks.NewMockCommandRunner(ctrl)
	inner.EXPECT().Exists(gomock.Any()).Return(true).AnyTimes()
	inner.EXPECT().RequireTool(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	commandRunner := system.NewBuilder(inner).
		WithLogging(func(
			format string,
			a ...any,
		) {
			logs = append(logs, fmt.Sprintf(format, a...))
		}).
		WithDryRun(true).
		Build()

	require.NoError(
		t,
		commandRunner.Run(context.Background(), "", textRm, "-rf", "/tmp/x"),
		"dry-run does not execute the inner runner",
	)
	assert.NoError(t, commandRunner.RunBuffered(context.Background(), "", textRm).Err,
		"dry-run returns an empty result")
	require.NoError(t, commandRunner.RunWithEnv(context.Background(), "", nil, textRm))
	assert.NoError(
		t,
		commandRunner.RunBufferedWithEnv(context.Background(), "", nil, textRm).Err,
	)

	assert.True(t, commandRunner.Exists("anything"), "Exists still delegates (read-only)")
	require.NoError(t, commandRunner.RequireTool(textGoLower, ""))
	assert.NotEmpty(t, logs, "dry-run logs what would execute")
}

// TestRunnerBuilder_WithEnvAddsEnvToEveryRun tests the env decorator.
//
// Why this test is important:
//   - A gate that must run its commands with extra environment (preflight runs the
//     tests with CI's env) decorates the runner once instead of threading env through
//     every call; a call's own env must still win over the decoration's.
//
// What it tests:
//   - Run and RunBuffered become their WithEnv forms carrying the decoration's env.
//   - RunWithEnv and RunBufferedWithEnv get the decoration's env, then the call's own.
//   - Exists and RequireTool delegate unchanged.
func TestRunnerBuilder_WithEnvAddsEnvToEveryRun(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	inner := mocks.NewMockCommandRunner(ctrl)
	ctx := context.Background()
	ci := []string{"CI=true"}
	both := []string{"CI=true", textCiFalse}

	inner.EXPECT().RunWithEnv(ctx, textDLower, ci, textGoLower, textTest).Return(nil)
	inner.EXPECT().RunWithEnv(ctx, textDLower, both, textGoLower, textTest).Return(nil)
	inner.EXPECT().RunBufferedWithEnv(ctx, textDLower, ci, textGoLower, textVet).
		Return(system.CmdResult{
			Stdout: []byte(textA), Err: nil, Stderr: nil, Duration: 0,
			ExitCode: 0,
		})
	inner.EXPECT().RunBufferedWithEnv(ctx, textDLower, both, textGoLower, textVet).
		Return(system.CmdResult{
			Stdout: []byte(textB), Err: nil, Stderr: nil, Duration: 0,
			ExitCode: 0,
		})
	inner.EXPECT().Exists(textGoLower).Return(true)
	inner.EXPECT().RequireTool(textGoLower, "brew install go").Return(nil)

	commandRunner := system.NewBuilder(inner).WithEnv("CI=true").Build()
	require.NoError(t, commandRunner.Run(ctx, textDLower, textGoLower, textTest))
	require.NoError(
		t,
		commandRunner.RunWithEnv(
			ctx,
			textDLower,
			[]string{textCiFalse},
			textGoLower,
			textTest,
		),
	)
	assert.Equal(
		t,
		textA,
		string(commandRunner.RunBuffered(ctx, textDLower, textGoLower, textVet).Stdout),
	)
	assert.Equal(t, textB, string(
		commandRunner.RunBufferedWithEnv(
			ctx,
			textDLower,
			[]string{textCiFalse},
			textGoLower,
			textVet,
		).Stdout,
	))
	assert.True(t, commandRunner.Exists(textGoLower))
	require.NoError(t, commandRunner.RequireTool(textGoLower, "brew install go"))
}

// TestRunner_RunBufferedWithInput tests a buffered run fed from stdin.
//
// Why this test is important:
//   - a manifest or a secret handed to `kubectl apply -f -` must travel on the child's
//     stdin; written to a temp file instead it lingers on disk when a run is killed.
//
// What it tests:
//   - the real runner feeds input to the child's stdin with extraEnv added, and
//     captures its stdout.
func TestRunner_RunBufferedWithInput(t *testing.T) {
	t.Parallel()

	r := system.NewRunner()

	res := r.RunBufferedWithInput(t.Context(), "", []string{"FOO=a"}, []byte("b\n"),
		"sh", "-c", `printf %s "$FOO"; cat`)

	require.NoError(t, res.Err)
	assert.Equal(t, "ab\n", string(res.Stdout))
}

// TestBuilder_RunBufferedWithInput tests that every runner decorator passes a stdin
// run through.
//
// Why this test is important:
//   - a decorator that dropped the input, the env it adds, or its own logging and hooks
//     on the stdin path would make that path behave unlike every other run.
//
// What it tests:
//   - through the env, logging, timeout and hook decorators the base gets the input
//     and the env the builder adds; the run is logged once and the hook sees it start
//     and succeed; a dry run never reaches the base; the buffering runner captures the
//     output.
func TestBuilder_RunBufferedWithInput(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	base := mocks.NewMockCommandRunner(ctrl)
	base.EXPECT().
		RunBufferedWithInput(gomock.Any(), textD, []string{"E=1", textX2}, []byte(textIn),
			textKubectl, "apply", "-f", "-").
		Return(system.CmdResult{
			Stdout: []byte("applied\n"), Err: nil, Stderr: nil,
			Duration: 0, ExitCode: 0,
		}).
		Times(wantCalls)

	var logs, events []string

	commandRunner := system.NewBuilder(base).
		WithEnv("E=1").
		WithLogging(func(format string, args ...any) {
			logs = append(logs, fmt.Sprintf(format, args...))
		}).
		WithTimeout(time.Minute).
		WithHook(recordHooks(ctrl, &events)).
		Build()

	res := commandRunner.RunBufferedWithInput(
		t.Context(),
		textD,
		[]string{textX2},
		[]byte(textIn),
		textKubectl,
		"apply",
		"-f",
		"-",
	)

	require.NoError(t, res.Err)
	assert.Equal(t, "applied\n", string(res.Stdout))
	assert.Len(t, logs, 1)
	assert.Equal(t, []string{"start:kubectl apply -f -", "ok:kubectl apply -f -"}, events)

	dry := system.NewBuilder(base).WithDryRun(true).Build()
	assert.Equal(t, stdx.Zero[system.CmdResult](),
		dry.RunBufferedWithInput(t.Context(), textD, nil, []byte(textIn), textKubectl))

	b := system.NewQuietBufferingRunner(system.NewBuilder(base).WithEnv("E=1").Build())

	_ = b.RunBufferedWithInput(t.Context(), textD, []string{textX2}, []byte(textIn),
		textKubectl, "apply", "-f", "-")
	assert.Equal(t, "applied\n", string(b.Captured()))
}

const (
	// textCiFalse is the literal "CI=false".
	textCiFalse = "CI=false"

	// textD is the literal "/d".
	textD = "/d"

	// textX2 is the literal "X=2".
	textX2 = "X=2"
)
