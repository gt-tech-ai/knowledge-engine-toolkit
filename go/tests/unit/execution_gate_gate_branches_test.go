package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/gate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestRunGateNoRunner_GuardRunnerRejectsEveryCommandCall tests that the no-op
// CommandRunner RunGateNoRunner supplies fails loudly on every method rather than
// silently succeeding, so a genuine command job mistakenly composed into a
// runner-less gate surfaces a programming error instead of a false pass.
//
// Why this test is important:
//   - RunGateNoRunner exists for Runnable-backed gates (e.g. DB seeding) that never
//     touch a CommandRunner. If a command job slipped in, a silently-succeeding
//     runner would report the command as passed without ever running it — the
//     most dangerous kind of false green. The guard's contract is that every
//     runner method reports the error, so the mistake cannot hide.
//
// What it tests:
//   - Through a probe job that invokes each CommandRunner method with the runner
//     RunGateNoRunner injected, Run/RunWithEnv/RequireTool return an error naming
//     the runner-less gate, RunBuffered/RunBufferedWithEnv return a CmdResult with
//     that error and ExitCode -1, and Exists reports false.
func TestRunGateNoRunner_GuardRunnerRejectsEveryCommandCall(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	var (
		runErr     error
		runEnvErr  error
		requireErr error
		buf        interfaces.CmdResult
		bufEnv     interfaces.CmdResult
		exists     bool
	)

	// The probe job receives the runner RunGateNoRunner injects (the guard
	// noRunner) and exercises every method, capturing each result for assertion
	// after the gate returns.
	probe := mocks.NewMockAnyJob(ctrl)
	probe.EXPECT().Meta().Return(types.JobMeta{Name: "probe"}).AnyTimes()
	probe.EXPECT().Execute(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(
			ctx context.Context,
			runner interfaces.CommandRunner,
			_ string,
		) (types.StepResults, error) {
			runErr = runner.Run(ctx, "/dir", "echo", "hi")
			runEnvErr = runner.RunWithEnv(ctx, "/dir", []string{"K=V"}, "echo", "hi")
			buf = runner.RunBuffered(ctx, "/dir", "echo", "hi")
			bufEnv = runner.RunBufferedWithEnv(ctx, "/dir", []string{"K=V"}, "echo", "hi")
			exists = runner.Exists("echo")
			requireErr = runner.RequireTool("echo", "install echo")
			return types.StepResults{{Name: "probe", Status: types.StatusPass}}, nil
		},
	).
		Times(1)

	g := gate.NewGate("no-runner-guard").Serial("probe", probe).Build()
	require.NoError(t, gate.RunGateNoRunner(context.Background(), g))

	require.Error(t, runErr, "Run must reject on a runner-less gate")
	assert.Contains(t, runErr.Error(), "runner-less gate")
	require.Error(t, runEnvErr, "RunWithEnv must reject on a runner-less gate")
	require.Error(t, requireErr, "RequireTool must reject on a runner-less gate")

	require.Error(t, buf.Err, "RunBuffered must carry the guard error")
	assert.Equal(t, -1, buf.ExitCode, "RunBuffered must report ExitCode -1")
	require.Error(t, bufEnv.Err, "RunBufferedWithEnv must carry the guard error")
	assert.Equal(t, -1, bufEnv.ExitCode, "RunBufferedWithEnv must report ExitCode -1")

	assert.False(t, exists, "Exists must report false on a runner-less gate")
}

// TestRunGate_BareFailureWithoutMessageStillFailsGate tests that a Fail result
// carrying no error string still fails the gate, producing a phase-level error.
//
// Why this test is important:
//   - A job can legitimately report StatusFail without an accompanying message
//     (the status is the signal). If the gate only failed when a failed step also
//     carried a non-empty Error, such a bare failure would pass the gate silently
//     — a false green. This pins that the status alone gates the run, and that the
//     aggregated error still names the failing phase even with no step detail to
//     fold in.
//
// What it tests:
//   - A single serial phase whose only result is a message-less StatusFail makes
//     RunGate return a non-nil error naming that phase.
func TestRunGate_BareFailureWithoutMessageStillFailsGate(t *testing.T) {
	t.Parallel()

	// A Fail result with an empty Error string — the phaseFailureError path that
	// collects no per-step messages and falls back to a bare phase error.
	bare := recordingJob(
		t, "bare",
		types.StepResults{{Name: "bare", Status: types.StatusFail}},
		nil,
	)

	g := gate.NewGate("bare-gate").Serial("checks", bare).Build()

	err := gate.RunGate(context.Background(), gateNopRunner(t), "/", g)
	require.Error(t, err, "a message-less Fail must still fail the gate")
	assert.Contains(
		t,
		err.Error(),
		"phase checks failed",
		"aggregated error should name the failing phase: %v",
		err,
	)
}
