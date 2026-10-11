package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/engine"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// sleepingJob returns a MockAnyJob named "j" whose Execute sleeps before returning
// the given results/err, so the surrounding timing logic measures a non-trivial
// elapsed. The runner arg is ignored (RunJobGroup is given a no-op runner).
func sleepingJob(
	ctrl *gomock.Controller,
	sleep time.Duration,
	results types.StepResults,
	err error,
) *mocks.MockAnyJob {
	job := mocks.NewMockAnyJob(ctrl)
	job.EXPECT().Meta().Return(types.JobMeta{Name: "j"}).AnyTimes()
	job.EXPECT().Execute(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, interfaces.CommandRunner, string) (types.StepResults, error) {
			time.Sleep(sleep)
			return results, err
		},
	).
		AnyTimes()
	return job
}

// --- FanOut duration stamping ---

// TestFanOut_StampsDurationWhenZero tests that FanOut measures each unit's
// wall-clock and stamps Duration when the producer left it at zero.
//
// Why this test is important:
//   - Per-unit durations are what the terminal UI and the CI step summary
//     report; if a producer that doesn't time itself were left at zero, every
//     such step would display 0s and the timing breakdown would be useless.
//
// What it tests:
//   - Every returned result whose fn did not set Duration comes back with a
//     positive Duration reflecting the elapsed time of the invocation.
func TestFanOut_StampsDurationWhenZero(t *testing.T) {
	results := engine.FanOut(
		context.Background(),
		[]int{0, 1},
		2,
		func(_ context.Context, _ int) types.StepResult {
			time.Sleep(2 * time.Millisecond)
			return types.StepResult{Status: types.StatusPass} // Duration left zero
		},
	)
	for i, r := range results {
		assert.Positive(t, r.Duration, "result %d: expected stamped Duration > 0", i)
	}
}

// TestFanOut_PreservesNonZeroDuration tests that FanOut leaves a producer's own
// Duration untouched instead of overwriting it with the engine's measurement.
//
// Why this test is important:
//   - Some producers report a finer-grained, more meaningful duration than the
//     coarse slot-to-return wall-clock (e.g. excluding setup). Clobbering it
//     would discard the more accurate figure and mislead the timing report.
//
// What it tests:
//   - A result returned with a non-zero Duration is reported back verbatim,
//     not replaced by FanOut's measured elapsed.
func TestFanOut_PreservesNonZeroDuration(t *testing.T) {
	const want = 42 * time.Second
	results := engine.FanOut(
		context.Background(),
		[]int{0},
		1,
		func(_ context.Context, _ int) types.StepResult {
			return types.StepResult{Status: types.StatusPass, Duration: want}
		},
	)
	assert.Equal(t, want, results[0].Duration)
}

// --- RunJobGroup duration backstop ---

// TestRunJobGroup_StampsDurationOnZeroResult tests that RunJobGroup backstops an
// untimed job result with the job-execute elapsed.
//
// Why this test is important:
//   - Single-op adapter jobs return a result without timing it themselves. If
//     RunJobGroup did not backstop the Duration, those phases would render 0s
//     in the run summary, making per-phase timing unreliable.
//
// What it tests:
//   - A job result that arrives with no Duration is reported with a positive
//     Duration drawn from the surrounding job.Execute elapsed.
func TestRunJobGroup_StampsDurationOnZeroResult(t *testing.T) {
	ctrl := gomock.NewController(t)
	job := sleepingJob(
		ctrl,
		3*time.Millisecond,
		types.StepResults{{Name: "j", Status: types.StatusPass}},
		nil,
	)
	results, err := engine.RunJobGroup(
		context.Background(),
		engineNopRunner(t),
		"/root",
		[]interfaces.AnyJob{job},
		engine.NopObserver{},
	)
	require.NoError(t, err)
	assert.Positive(t, results[0].Duration, "expected stamped Duration > 0")
}

// TestRunJobGroup_PreservesNonZeroDuration tests that RunJobGroup's job-level
// backstop never clobbers a finer-grained duration already on a result.
//
// Why this test is important:
//   - FanOut stamps an accurate per-unit duration; the coarse job-execute
//     elapsed covers the whole job. Overwriting the per-unit value with the
//     job total would collapse multi-unit timing granularity to a single
//     number and misreport how long each unit actually took.
//
// What it tests:
//   - A result that already carries a non-zero Duration is reported unchanged,
//     not overwritten by the job-execute elapsed.
func TestRunJobGroup_PreservesNonZeroDuration(t *testing.T) {
	const want = 99 * time.Second
	ctrl := gomock.NewController(t)
	job := sleepingJob(
		ctrl,
		2*time.Millisecond,
		types.StepResults{{Name: "j", Status: types.StatusPass, Duration: want}},
		nil,
	)
	results, err := engine.RunJobGroup(
		context.Background(),
		engineNopRunner(t),
		"/root",
		[]interfaces.AnyJob{job},
		engine.NopObserver{},
	)
	require.NoError(t, err)
	assert.Equal(t, want, results[0].Duration)
}

// TestRunJobGroup_StampsDurationOnErrorResult tests that the synthetic Fail
// result RunJobGroup builds when job.Execute errors is itself timed.
//
// Why this test is important:
//   - When a job's discovery or pre-execution fails, RunJobGroup fabricates a
//     single Fail result so the gate sees the failure. That synthetic result
//     must still carry a duration, or failed phases would render 0s and look
//     like they never ran — masking how long the failing work took.
//
// What it tests:
//   - An erroring job yields exactly one StatusFail result, and that result
//     comes back with a positive Duration.
func TestRunJobGroup_StampsDurationOnErrorResult(t *testing.T) {
	ctrl := gomock.NewController(t)
	job := sleepingJob(ctrl, 3*time.Millisecond, nil, apperr.Sentinel("boom"))
	results, err := engine.RunJobGroup(
		context.Background(),
		engineNopRunner(t),
		"/root",
		[]interfaces.AnyJob{job},
		engine.NopObserver{},
	)
	require.NoError(t, err)
	require.Len(t, results, 1, "expected 1 Fail result, got %v", results)
	require.Equal(
		t,
		types.StatusFail,
		results[0].Status,
		"expected 1 Fail result, got %v",
		results,
	)
	assert.Positive(
		t,
		results[0].Duration,
		"expected stamped Duration > 0 on error result",
	)
}
