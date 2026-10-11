package unit_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/system"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestBuilderNoDecorators tests that a Builder with no decorators returns a
// runner that delegates straight to the base.
//
// Why this test is important:
//   - The undecorated path is the default; if Build() silently swallowed or
//     rewrapped the base, every command would route through dead middleware.
//
// What it tests:
//   - Run on the built runner reaches the underlying base runner exactly once.
func TestBuilderNoDecorators(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	base := mocks.NewMockCommandRunner(ctrl)
	// The Times(1) expectation IS the "base.Run was called" assertion (verified at
	// the controller's finish), replacing the old fake's recorded runCalled flag.
	base.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil).Times(1)

	r := system.NewBuilder(base).Build()

	require.NoError(t, r.Run(context.Background(), "", "echo"))
}

// TestBuilderWithLogging tests that the logging decorator emits one entry per
// Run carrying the command name and the [runner] prefix.
//
// Why this test is important:
//   - The CLI's --verbose command echo depends on this decorator; a missing or
//     malformed log line would hide which subprocess actually ran.
//
// What it tests:
//   - One Run reaches the base once and produces exactly one log entry containing
//     "[runner]" and the command name.
func TestBuilderWithLogging(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	base := mocks.NewMockCommandRunner(ctrl)
	base.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil).Times(1)
	var logs []string
	logf := func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	r := system.NewBuilder(base).WithLogging(logf).Build()

	require.NoError(t, r.Run(context.Background(), "/tmp", "go", "build"))

	require.Len(t, logs, 1, "expected 1 log entry")
	assert.Contains(t, logs[0], "[runner]", "log should contain [runner] prefix")
	assert.Contains(t, logs[0], "go", "log should contain command name")
}

// TestBuilderWithLogging_RunBuffered tests that the logging decorator wraps the
// buffered path: it delegates once and returns the base result intact while
// logging.
//
// Why this test is important:
//   - RunBuffered carries the command's stdout/duration that callers parse;
//     the decorator must not drop or mutate that result while adding a log line.
//
// What it tests:
//   - One RunBuffered call delegates once to the base, returns its duration
//     unchanged, and emits one log entry.
func TestBuilderWithLogging_RunBuffered(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	base := mocks.NewMockCommandRunner(ctrl)
	base.EXPECT().RunBuffered(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(system.CmdResult{Duration: 42 * time.Millisecond}).Times(1)
	var logs []string
	logf := func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	r := system.NewBuilder(base).WithLogging(logf).Build()
	cr := r.RunBuffered(context.Background(), "/tmp", "go", "test")

	assert.Equal(t, 42*time.Millisecond, cr.Duration, "expected 42ms duration")
	require.Len(t, logs, 1, "expected 1 log entry")
}

// TestBuilderWithLogging_RunWithEnv tests that the logging decorator covers the
// env-injecting path, delegating to the base and logging once.
//
// Why this test is important:
//   - Commands needing extra env (e.g. scoped GOWORK) go through RunWithEnv; the
//     decorator must instrument it just like plain Run, not bypass logging.
//
// What it tests:
//   - One RunWithEnv call reaches the base once and produces one log entry.
func TestBuilderWithLogging_RunWithEnv(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	base := mocks.NewMockCommandRunner(ctrl)
	base.EXPECT().RunWithEnv(
		gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
	).Return(nil).Times(1)
	var logs []string
	logf := func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	r := system.NewBuilder(base).WithLogging(logf).Build()
	require.NoError(t, r.RunWithEnv(
		context.Background(),
		"/tmp",
		[]string{"FOO=bar"},
		"go",
		"build",
	))

	require.Len(t, logs, 1, "expected 1 log")
}

// TestBuilderWithDryRun tests that dry-run suppresses mutating execution while
// letting read-only checks pass through and logging the intended command.
//
// Why this test is important:
//   - Dry-run is the safety net for destructive commands; if Run still reached
//     the base, a `--dry-run` invocation could execute a real `rm -rf`.
//
// What it tests:
//   - Run does not call the base (no Run expectation is set, so gomock would fail
//     on any call), Exists still delegates to the base, and a "[dry-run]" log entry
//     records what would have executed.
func TestBuilderWithDryRun(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	base := mocks.NewMockCommandRunner(ctrl)
	// No Run expectation: dry-run must not call base.Run, and gomock fails the test
	// on any unexpected call — the assertion that the base is never executed.
	base.EXPECT().Exists(gomock.Any()).Return(true).AnyTimes()
	var logs []string
	logf := func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	r := system.NewBuilder(base).
		WithLogging(logf).
		WithDryRun(true).
		Build()

	require.NoError(t, r.Run(context.Background(), "/tmp", "rm", "-rf", "/"),
		"dry-run should not call base.Run")

	assert.True(t, r.Exists("go"), "Exists should delegate to base even in dry-run")

	found := false
	for _, l := range logs {
		if strings.Contains(l, "[dry-run]") {
			found = true
		}
	}
	assert.True(t, found, "expected a [dry-run] log entry")
}

// TestBuilderWithDryRun_RunBuffered tests that dry-run also short-circuits the
// buffered path, returning a clean zero result without touching the base.
//
// Why this test is important:
//   - Buffered commands (e.g. those reading output) must be skipped under dry-run
//     too; otherwise a "preview" run would still execute and mutate state.
//
// What it tests:
//   - RunBuffered makes no base call (no RunBuffered expectation is set) and
//     returns a nil-error CmdResult.
func TestBuilderWithDryRun_RunBuffered(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	// No RunBuffered expectation: dry-run must not call it; gomock fails on any call.
	base := mocks.NewMockCommandRunner(ctrl)

	r := system.NewBuilder(base).WithDryRun(true).Build()
	cr := r.RunBuffered(context.Background(), "/tmp", "go", "test")

	assert.NoError(t, cr.Err, "dry-run RunBuffered should return nil error")
}

// TestBuilderWithTimeout tests that the timeout decorator cancels a command that
// outlives its deadline.
//
// Why this test is important:
//   - Without an enforced timeout, a hung subprocess would stall the whole CLI
//     gate indefinitely; the deadline is what guarantees forward progress.
//
// What it tests:
//   - A runner that blocks 500ms under a 10ms timeout returns a (deadline) error.
func TestBuilderWithTimeout(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	// A mock whose Run blocks until the context is cancelled, so the timeout
	// decorator's deadline is what unblocks it — reproducing the old slow runner.
	slow := mocks.NewMockCommandRunner(ctrl)
	slow.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, _, _ string, _ ...string) error {
			select {
			case <-time.After(500 * time.Millisecond):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}).AnyTimes()

	r := system.NewBuilder(slow).
		WithTimeout(10 * time.Millisecond).
		Build()

	err := r.Run(context.Background(), "", "sleep")
	assert.Error(t, err, "expected timeout error")
}

// TestBuilderComposition tests that timeout and logging decorators stack
// correctly and a non-expiring command still reaches the base while being logged.
//
// Why this test is important:
//   - Real runners are built with several decorators at once; an ordering or
//     wrapping bug could swallow the call or skip logging when both are present.
//
// What it tests:
//   - With timeout + logging applied, a fast Run reaches the base once and emits
//     one log entry.
func TestFoundationBuilderComposition(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	base := mocks.NewMockCommandRunner(ctrl)
	base.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil).Times(1)
	var logs []string
	logf := func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	r := system.NewBuilder(base).
		WithTimeout(5 * time.Second).
		WithLogging(logf).
		Build()

	require.NoError(t, r.Run(context.Background(), "", "echo", "hello"))
	require.Len(t, logs, 1, "expected 1 log")
}
