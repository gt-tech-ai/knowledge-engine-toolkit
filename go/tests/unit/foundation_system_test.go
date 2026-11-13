package unit_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/system"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestRunner_RealCommands tests the real Runner against the always-present `go`
// binary and a missing binary.
//
// Why this test is important:
//   - Runner executes every external tool the CLI shells out to; if it
//     mis-reported success/failure or exit codes, every gate's pass/fail signal
//     would be wrong. Exercising it against real commands proves the behavior.
//
// What it tests:
//   - Run succeeds on a real command and errors on a missing binary; RunBuffered
//     captures stdout and exit 0 for success, and exit -1 (never started) for a
//     missing binary; the *WithEnv variants run the extra-env path.
func TestRunner_RealCommands(t *testing.T) {
	t.Parallel()

	runner := system.NewRunner()
	ctx := context.Background()

	require.NoError(t, runner.Run(ctx, "", textGoLower, textVersion))
	require.Error(t, runner.Run(ctx, "", textDefinitelyNotAReal))

	res := runner.RunBuffered(ctx, "", textGoLower, textVersion)
	require.NoError(t, res.Err)
	assert.Equal(t, 0, res.ExitCode)
	assert.Contains(t, string(res.Stdout), "go version")

	bad := runner.RunBuffered(ctx, "", textDefinitelyNotAReal)
	require.Error(t, bad.Err)
	assert.Equal(t, -1, bad.ExitCode, "missing binary never starts -> exit -1")

	require.NoError(
		t,
		runner.RunWithEnv(ctx, "", []string{textFooBar}, textGoLower, textVersion),
	)

	envRes := runner.RunBufferedWithEnv(
		ctx,
		"",
		[]string{textFooBar},
		textGoLower,
		textVersion,
	)
	assert.NoError(t, envRes.Err)
}

// TestRunner_ExistsAndRequireTool tests PATH lookup and the tool-required guard.
//
// Why this test is important:
//   - RequireTool gates every command behind a clear "install X" error; a false
//     positive would let a gate run a missing tool and fail cryptically.
//
// What it tests:
//   - Exists is true for `go` and false for a missing binary; RequireTool returns
//     nil for a present tool and an error carrying the install hint for a missing one.
func TestRunner_ExistsAndRequireTool(t *testing.T) {
	t.Parallel()

	runner := system.NewRunner()
	assert.True(t, runner.Exists(textGoLower))
	assert.False(t, runner.Exists(textDefinitelyNotAReal))

	require.NoError(t, runner.RequireTool(textGoLower, "install go"))

	err := runner.RequireTool(textDefinitelyNotAReal, "brew install xyz")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "brew install xyz", "install hint surfaced")
}

// TestRunner_ContextCancelled tests that a cancelled context surfaces as an error.
//
// Why this test is important:
//   - Ctrl+C / timeouts cancel the context; Runner must abort and report rather
//     than hang or claim success.
//
// What it tests:
//   - Run with an already-cancelled context returns an error.
func TestRunner_ContextCancelled(t *testing.T) {
	t.Parallel()

	r := system.NewRunner()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(
		t,
		r.Run(ctx, "", textGoLower, textVersion),
		"cancelled context -> error",
	)
}

// TestBufferingRunner tests that BufferingRunner captures inner output, wraps
// failures, and delegates the non-running methods.
//
// Why this test is important:
//   - BufferingRunner is what concurrent gates use so command output is captured
//     for failure diagnostics (StepResult.Detail) instead of interleaving on the
//     terminal; dropped capture would lose the diagnostics, and a swallowed inner
//     error would hide a failed step.
//
// What it tests:
//   - RunBuffered/RunBufferedWithEnv accumulate stdout+stderr into Captured();
//     Run returns nil on inner success and a wrapped error (naming the command) on
//     inner failure while still capturing output, its stderr tail under
//     DetailStderrTail as a streamed Run's; RunWithEnv delegates to the
//     inner env path; Exists/RequireTool delegate; both quiet and replay
//     constructors work.
func TestBufferingRunner(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)

	inner := mocks.NewMockCommandRunner(ctrl)
	inner.EXPECT().
		RunBuffered(gomock.Any(), "", textX).
		Return(system.CmdResult{
			Stdout: []byte("out-"), Stderr: []byte("err"), Err: nil,
			Duration: 0, ExitCode: 0,
		})
	inner.EXPECT().
		RunBufferedWithEnv(gomock.Any(), "", []string{textAB}, textX).
		Return(system.CmdResult{
			Stdout: []byte("out-"), Stderr: []byte("err"), Err: nil,
			Duration: 0, ExitCode: 0,
		})
	inner.EXPECT().Exists("anything").Return(true)
	inner.EXPECT().RequireTool(textX, "").Return(nil)

	bufferingRunner := system.NewQuietBufferingRunner(inner)

	res := bufferingRunner.RunBuffered(context.Background(), "", textX)
	assert.Equal(t, "out-", string(res.Stdout))
	assert.Equal(
		t,
		"out-err",
		string(bufferingRunner.Captured()),
		"RunBuffered accumulates stdout+stderr",
	)

	_ = bufferingRunner.RunBufferedWithEnv(
		context.Background(),
		"",
		[]string{textAB},
		textX,
	)
	assert.Equal(t, "out-errout-err", string(bufferingRunner.Captured()))

	assert.True(t, bufferingRunner.Exists("anything"), "Exists delegates")
	require.NoError(t, bufferingRunner.RequireTool(textX, ""), "RequireTool delegates")

	testBufferingRunnerFailure(t, ctrl)

	// Run (no env) success path.
	okInner := mocks.NewMockCommandRunner(ctrl)

	testBufferingRunnerCommands(t, ctrl, okInner)
}

// testBufferingRunnerFailure continues TestBufferingRunner: Run (no env) wraps a
// failing inner result, still captures its output, and carries its stderr tail.
func testBufferingRunnerFailure(t *testing.T, ctrl *gomock.Controller) {
	t.Helper()

	failingInner := mocks.NewMockCommandRunner(ctrl)
	failingInner.EXPECT().
		RunBuffered(gomock.Any(), "", textTool, "arg").
		Return(system.CmdResult{
			Stderr: []byte(textBoom), Err: apperr.Sentinel("exit 1"),
			Stdout: nil, Duration: 0, ExitCode: 0,
		})

	failing := system.NewQuietBufferingRunner(failingInner)
	err := failing.Run(context.Background(), "", textTool, "arg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tool arg", "wrapped error names the command")
	assert.Equal(t, textBoom, string(failing.Captured()))

	var failure *apperr.AppError

	require.ErrorAs(t, err, &failure)
	assert.Equal(t, textBoom, failure.Details[interfaces.DetailStderrTail],
		"the failure carries its stderr tail as a streamed Run's does")
}

// testBufferingRunnerCommands continues TestBufferingRunner: commands.
func testBufferingRunnerCommands(
	t *testing.T,
	ctrl *gomock.Controller,
	okInner *mocks.MockCommandRunner,
) {
	t.Helper()

	okInner.EXPECT().
		RunBuffered(gomock.Any(), "", textX).
		Return(system.CmdResult{
			Stdout: []byte("hi"), Err: nil, Stderr: nil, Duration: 0,
			ExitCode: 0,
		})

	okRunner := system.NewQuietBufferingRunner(okInner)
	require.NoError(t, okRunner.Run(context.Background(), "", textX))

	// RunWithEnv delegates to the inner env path.
	envInner := mocks.NewMockCommandRunner(ctrl)
	envInner.EXPECT().
		RunWithEnv(gomock.Any(), "", []string{textAB}, textX).
		Return(nil)

	envRunner := system.NewQuietBufferingRunner(envInner)
	require.NoError(
		t,
		envRunner.RunWithEnv(context.Background(), "", []string{textAB}, textX),
	)

	// Non-quiet (replay) constructor exercises the terminal-replay branches.
	replayInner := mocks.NewMockCommandRunner(ctrl)
	replayInner.EXPECT().
		RunBuffered(gomock.Any(), "", textX).
		Return(system.CmdResult{
			Stdout: []byte(textO), Stderr: []byte(textE), Err: nil,
			Duration: 0, ExitCode: 0,
		})

	replay := system.NewBufferingRunner(replayInner)
	require.NoError(t, replay.Run(context.Background(), "", textX))

	replayFailInner := mocks.NewMockCommandRunner(ctrl)
	replayFailInner.EXPECT().
		RunBuffered(gomock.Any(), "", textX).
		Return(system.CmdResult{
			Err: apperr.Sentinel(textX), Stdout: nil, Stderr: nil,
			Duration: 0, ExitCode: 0,
		})

	replayFail := system.NewBufferingRunner(replayFailInner)
	require.Error(t, replayFail.Run(context.Background(), "", textX))

	replayEnvInner := mocks.NewMockCommandRunner(ctrl)
	replayEnvInner.EXPECT().
		RunWithEnv(gomock.Any(), "", []string{textAB}, textX).
		Return(nil)

	replayEnv := system.NewBufferingRunner(replayEnvInner)
	require.NoError(
		t,
		replayEnv.RunWithEnv(context.Background(), "", []string{textAB}, textX),
	)
}

// captureProcessStdio swaps os.Stdout and os.Stderr for pipes until the returned
// function is called, which restores them and returns what each pipe received. The
// originals are also restored in t.Cleanup, so a failing assertion never leaks the swap.
// The process stdio is global, so callers run through fixtures.ReexecTest.
func captureProcessStdio(t *testing.T) func() (stdout, stderr string) {
	t.Helper()

	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	require.NoError(t, err)

	errR, errW, err := os.Pipe()
	require.NoError(t, err)

	os.Stdout, os.Stderr = outW, errW

	restore := func() { os.Stdout, os.Stderr = oldOut, oldErr }
	t.Cleanup(restore)

	return func() (string, string) {
		restore()
		require.NoError(t, outW.Close())
		require.NoError(t, errW.Close())

		gotOut, err := io.ReadAll(outR)
		require.NoError(t, err)

		gotErr, err := io.ReadAll(errR)
		require.NoError(t, err)

		return string(gotOut), string(gotErr)
	}
}

// TestRunner_WritesChildOutputToInjectedWriters tests that a Runner built with
// WithOutput streams a child's stdout and stderr to the injected writers.
//
// Why this test is important:
//   - A front-end that owns the process stdout (a stdio protocol server) cannot let a
//     child process write there; injecting the writers is how it runs tools safely.
//   - The builder decorators sit between callers and the runner, so the writers must
//     survive the full decorator chain.
//
// What it tests:
//   - A child's stdout lands in the injected stdout writer and its stderr in the
//     injected stderr writer, directly and through NewBuilder(...).Build() with every
//     decorator, and nothing reaches the process's own stdout or stderr.
func TestRunner_WritesChildOutputToInjectedWriters(t *testing.T) {
	t.Parallel()

	if !fixtures.InReexec() {
		fixtures.ReexecTest(t, "TestRunner_WritesChildOutputToInjectedWriters")

		return
	}

	ctx := context.Background()

	var out, errOut bytes.Buffer

	runner := system.NewRunner(system.WithOutput(&out, &errOut))
	decorated := system.NewBuilder(runner).
		WithLogging(func(string, ...any) {}).
		WithTimeout(time.Minute).
		WithDryRun(false).
		Build()
	stop := captureProcessStdio(t)

	require.NoError(t, runner.Run(ctx, "", textGoLower, textEnv, textGoversion))

	direct := out.String()

	require.Error(t, runner.Run(ctx, "", textGoLower, "definitely-not-a-subcommand"))
	require.NoError(t, decorated.Run(ctx, "", textGoLower, textEnv, textGoversion))

	procOut, procErr := stop()

	assert.Contains(t, direct, textGo1)
	assert.Equal(
		t,
		wantCountWriters,
		strings.Count(out.String(), textGo1),
		"decorated run reaches the injected stdout",
	)
	assert.NotEmpty(t, errOut.String())
	assert.Empty(t, procOut)
	assert.Empty(t, procErr)
}

// TestRunner_DefaultsToProcessStdio tests that a Runner built without options
// streams a child's output to the process's stdout, as it always has.
//
// Why this test is important:
//   - Every existing caller relies on tool output reaching the terminal; the writer
//     option must not change behavior for callers that don't pass it.
//
// What it tests:
//   - NewRunner() streams a child's stdout to os.Stdout as it is at Run time — so a
//     runner built before os.Stdout is reassigned still follows it.
func TestRunner_DefaultsToProcessStdio(t *testing.T) {
	t.Parallel()

	if !fixtures.InReexec() {
		fixtures.ReexecTest(t, "TestRunner_DefaultsToProcessStdio")

		return
	}

	r := system.NewRunner()
	stop := captureProcessStdio(t)

	require.NoError(
		t,
		r.Run(context.Background(), "", textGoLower, textEnv, textGoversion),
	)

	procOut, _ := stop()

	assert.Contains(t, procOut, textGo1)
}

const (
	// wantCountWriters is the expected count writers.
	wantCountWriters = 2
)

const (
	// textDefinitelyNotAReal is the literal "definitely-not-a-real-binary-xyz".
	textDefinitelyNotAReal = "definitely-not-a-real-binary-xyz"

	// textGo1 is the literal "go1.".
	textGo1 = "go1."

	// textGoversion is the literal "GOVERSION".
	textGoversion = "GOVERSION"
)

// Literals for TestRunner_FailureLeadsWithProgramAndStatus.
const (
	// textNoSuchGoCmd is a go subcommand that does not exist, so go exits 2.
	textNoSuchGoCmd = "no-such-subcommand"

	// textLongFlag pads the argv so a truncating display would cut it.
	textLongFlag = "--a-flag-long-enough-that-a-truncated-display-cuts-the-argv-off"

	// textGoExit2Prefix is how the failure message must begin.
	textGoExit2Prefix = "go exited with status 2"
)

// TestRunner_FailureLeadsWithProgramAndStatus tests that a command that exits
// non-zero yields an error message that starts with the program and its exit
// status, with the full argv after it.
//
// Why this test is important:
//   - CLIs print these errors on one truncated line; a message that leads with the
//     full argv (`docker compose --project-name … -f /U...`) hides the exit status,
//     which is the one fact the reader needs.
//
// What it tests:
//   - Runner.Run and a quiet BufferingRunner over a real Runner both return an
//     INTERNAL AppError whose Message begins "go exited with status 2" and still
//     names the argv.
func TestRunner_FailureLeadsWithProgramAndStatus(t *testing.T) {
	t.Parallel()

	runners := map[string]system.CommandRunner{
		"runner":    system.NewRunner(),
		"buffering": system.NewQuietBufferingRunner(system.NewRunner()),
	}

	for name, runner := range runners {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := runner.Run(
				context.Background(), "", textGoLower, textNoSuchGoCmd, textLongFlag,
			)

			var appErr *apperr.AppError

			require.ErrorAs(t, err, &appErr)
			assert.Equal(t, apperr.CodeInternal, appErr.Code)
			assert.True(t, strings.HasPrefix(appErr.Message, textGoExit2Prefix),
				"message %q should lead with the program and status", appErr.Message)
			assert.Contains(t, appErr.Message, textLongFlag)
		})
	}
}

// Literals for TestRunner_StreamedFailureCarriesStderrTail.
const (
	// textGoUsageLine is the last line go writes to stderr for an unknown command.
	textGoUsageLine = "Run 'go help' for usage."

	// longSubcommandLen makes go's stderr longer than the runner keeps.
	longSubcommandLen = 10_000
)

// TestRunner_StreamedFailureCarriesStderrTail tests that a streamed command that
// fails returns the end of its standard error on the error, while still streaming it.
//
// Why this test is important:
//   - A streamed command writes its stderr to the terminal, not to a buffer, so a
//     caller that reports the failure afterwards (a procedure's failed row, a run
//     summary) has only "docker exited with status 1 (command: …)" and never the
//     cause compose printed. The tail must ride on the error.
//   - The tail is bounded: a command that floods stderr must not put megabytes into
//     an error that is logged and rendered.
//
// What it tests:
//   - RunWithEnv of a failing go command streams its stderr to the configured writer
//     and returns an AppError whose DetailStderrTail detail is that stderr, trimmed.
//   - With stderr longer than the bound, the detail is shorter than what streamed and
//     keeps its end (go's closing usage line).
//   - A command that succeeds returns no error.
func TestRunner_StreamedFailureCarriesStderrTail(t *testing.T) {
	t.Parallel()

	for name, subcommand := range map[string]string{
		"short": textNoSuchGoCmd,
		"long":  strings.Repeat("x", longSubcommandLen),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var streamed bytes.Buffer

			runner := system.NewRunner(system.WithOutput(io.Discard, &streamed))
			err := runner.RunWithEnv(t.Context(), "", nil, textGoLower, subcommand)

			var appErr *apperr.AppError

			require.ErrorAs(t, err, &appErr)

			tail := appErr.Details[interfaces.DetailStderrTail]
			assert.True(t, strings.HasSuffix(tail, textGoUsageLine), "tail %q", tail)

			if name == "short" {
				assert.Equal(t, strings.TrimSpace(streamed.String()), tail)
			} else {
				assert.Less(t, len(tail), streamed.Len(), "the tail is bounded")
			}
		})
	}

	require.NoError(t, system.NewRunner(system.WithOutput(io.Discard, io.Discard)).
		RunWithEnv(t.Context(), "", nil, textGoLower, "version"))
}
