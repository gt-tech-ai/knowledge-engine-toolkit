package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/gate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// sleepyJob returns a MockAnyJob named name whose Execute sleeps 2ms before
// returning a single Pass result, so the gate's phase/gate wall-clock measurement
// is non-trivial.
func sleepyJob(t *testing.T, name string) interfaces.AnyJob {
	t.Helper()
	return recordingJob(t, name, passResults(name), func() {
		time.Sleep(2 * time.Millisecond)
	})
}

// TestGate_SerialPhaseFiresPhaseCompleteOnceWithElapsed tests that a serial
// phase fires OnPhaseComplete exactly once, carrying every job's result and a
// positive measured wall-clock for both the phase and the gate.
//
// Why this test is important:
//   - A serial phase runs one RunJobGroup call per job, so it is the case where
//     a naive implementation would fire OnPhaseComplete per job (duplicating the
//     phase line) or report only the last job's result. The CLI's clean
//     Homebrew-style renderer and CI step summary depend on exactly one phase
//     event carrying the full aggregate.
//   - Durations are surfaced to the user and the GitHub step summary; a zero or
//     unmeasured elapsed would mislead anyone reading the timing report.
//
// What it tests:
//   - OnPhaseComplete is invoked exactly once for a three-job serial phase.
//   - That single callback carries all three step results.
//   - Both the phase elapsed and the gate elapsed are strictly positive.
func TestGate_SerialPhaseFiresPhaseCompleteOnceWithElapsed(t *testing.T) {
	ctrl := gomock.NewController(t)
	var phaseResults types.StepResults
	var phaseElapsed, gateElapsed time.Duration
	obs := mocks.NewMockExecutionObserver(ctrl)
	obs.EXPECT().OnGateStart(gomock.Any(), gomock.Any()).AnyTimes()
	obs.EXPECT().OnPhaseStart(gomock.Any(), gomock.Any()).AnyTimes()
	obs.EXPECT().OnStepStart(gomock.Any(), gomock.Any()).AnyTimes()
	obs.EXPECT().OnStepComplete(gomock.Any()).AnyTimes()
	// Times(1) is the "OnPhaseComplete fires exactly once" assertion; Do captures
	// the aggregate results + elapsed for the remaining assertions.
	obs.EXPECT().OnPhaseComplete(gomock.Any(), gomock.Any(), gomock.Any()).Do(
		func(_ string, results types.StepResults, elapsed time.Duration) {
			phaseResults = results
			phaseElapsed = elapsed
		},
	).Times(1)
	obs.EXPECT().OnGateComplete(gomock.Any(), gomock.Any(), gomock.Any()).Do(
		func(_ string, _ types.StepResults, elapsed time.Duration) {
			gateElapsed = elapsed
		},
	).AnyTimes()

	g := gate.New("g").
		Serial("phase", sleepyJob(t, "a"), sleepyJob(t, "b"), sleepyJob(t, "c")).
		Observe(obs)

	require.NoError(t, g.Run(context.Background(), gateNopRunner(t), "/"))

	assert.Len(
		t,
		phaseResults,
		3,
		"expected the single phase-complete to carry all 3 results",
	)
	assert.Positive(t, phaseElapsed, "expected positive phase elapsed")
	assert.Positive(t, gateElapsed, "expected positive gate elapsed")
}
