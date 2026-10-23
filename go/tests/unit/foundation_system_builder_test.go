package unit_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/system"
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
		Return(system.CmdResult{Stdout: []byte(stdout)}).AnyTimes()
	inner.EXPECT().RunBufferedWithEnv(
		gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
	).Return(system.CmdResult{Stdout: []byte(stdout)}).AnyTimes()
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
	inner := okInner(ctrl, "ok")

	r := system.NewBuilder(inner).
		WithHook(hook).
		WithTimeout(time.Second).
		WithLogging(logf).
		Build()

	require.NoError(t, r.Run(context.Background(), "d", "go", "version"))
	_ = r.RunBuffered(context.Background(), "d", "go", "list")
	require.NoError(
		t,
		r.RunWithEnv(context.Background(), "d", []string{"A=b"}, "go", "env"),
	)
	_ = r.RunBufferedWithEnv(context.Background(), "d", nil, "go", "x")
	assert.True(t, r.Exists("go"), "Exists delegates through the chain")
	require.NoError(t, r.RequireTool("go", ""))

	assert.NotEmpty(t, logs, "logging decorator records each command")
	assert.Contains(t, events, "start:go version")
	assert.Contains(t, events, "ok:go version", "success fires OnSuccess")

	// Failing inner fires OnFailure for both Run and RunBuffered.
	var failEvents []string
	failHook := recordHooks(ctrl, &failEvents)
	failInner := mocks.NewMockCommandRunner(ctrl)
	boom := apperr.Sentinel("boom")
	failInner.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(boom).AnyTimes()
	failInner.EXPECT().RunWithEnv(
		gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
	).Return(boom).AnyTimes()
	failInner.EXPECT().
		RunBuffered(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(system.CmdResult{Err: boom}).
		AnyTimes()
	failInner.EXPECT().RunBufferedWithEnv(
		gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
	).Return(system.CmdResult{Err: boom}).AnyTimes()
	fr := system.NewBuilder(failInner).WithHook(failHook).Build()
	require.Error(t, fr.Run(context.Background(), "", "x", "y"))
	require.Error(t, fr.RunBuffered(context.Background(), "", "z").Err)
	require.Error(t, fr.RunWithEnv(context.Background(), "", nil, "x"))
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
	r := system.NewBuilder(inner).
		WithLogging(func(
			format string,
			a ...any,
		) {
			logs = append(logs, fmt.Sprintf(format, a...))
		}).
		WithDryRun(true).
		Build()

	require.NoError(t, r.Run(context.Background(), "", "rm", "-rf", "/tmp/x"),
		"dry-run does not execute the inner runner")
	assert.NoError(t, r.RunBuffered(context.Background(), "", "rm").Err,
		"dry-run returns an empty result")
	require.NoError(t, r.RunWithEnv(context.Background(), "", nil, "rm"))
	assert.NoError(t, r.RunBufferedWithEnv(context.Background(), "", nil, "rm").Err)

	assert.True(t, r.Exists("anything"), "Exists still delegates (read-only)")
	require.NoError(t, r.RequireTool("go", ""))
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
	both := []string{"CI=true", "CI=false"}
	inner.EXPECT().RunWithEnv(ctx, "d", ci, "go", "test").Return(nil)
	inner.EXPECT().RunWithEnv(ctx, "d", both, "go", "test").Return(nil)
	inner.EXPECT().RunBufferedWithEnv(ctx, "d", ci, "go", "vet").
		Return(system.CmdResult{Stdout: []byte("a")})
	inner.EXPECT().RunBufferedWithEnv(ctx, "d", both, "go", "vet").
		Return(system.CmdResult{Stdout: []byte("b")})
	inner.EXPECT().Exists("go").Return(true)
	inner.EXPECT().RequireTool("go", "brew install go").Return(nil)

	r := system.NewBuilder(inner).WithEnv("CI=true").Build()
	require.NoError(t, r.Run(ctx, "d", "go", "test"))
	require.NoError(t, r.RunWithEnv(ctx, "d", []string{"CI=false"}, "go", "test"))
	assert.Equal(t, "a", string(r.RunBuffered(ctx, "d", "go", "vet").Stdout))
	assert.Equal(t, "b", string(
		r.RunBufferedWithEnv(ctx, "d", []string{"CI=false"}, "go", "vet").Stdout))
	assert.True(t, r.Exists("go"))
	require.NoError(t, r.RequireTool("go", "brew install go"))
}
