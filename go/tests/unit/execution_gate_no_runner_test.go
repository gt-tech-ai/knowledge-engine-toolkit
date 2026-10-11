package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/gate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/job"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestRunGateNoRunner_RunsRunnableJobs tests that a gate composed of runner-less
// Runnables (lifted via job.Adapt) runs to success through RunGateNoRunner without
// a CommandRunner.
//
// Why this test is important:
//   - Runner-less gates (e.g. DB seeding) compose Runnables; RunGateNoRunner must
//     execute them without threading a CommandRunner, so no consumer passes a nil
//     runner into the gate API
//
// What it tests:
//   - A Serial phase of Adapt(Runnable) units runs: RunGateNoRunner returns nil and
//     every Runnable's Run executed
func TestRunGateNoRunner_RunsRunnableJobs(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)

	// Each Runnable's Run is expected exactly once — the gomock Times(1)
	// expectation (verified at ctrl.Finish) replaces the old boolean `ran` flag as
	// the proof that RunGateNoRunner executed both units.
	runnableA := mocks.NewMockRunnable(ctrl)
	runnableA.EXPECT().Meta().Return(types.JobMeta{Name: "a"}).AnyTimes()
	runnableA.EXPECT().Run(gomock.Any()).
		Return(types.StepResults{{Name: "a", Status: types.StatusPass}}, nil).
		Times(1)

	runnableB := mocks.NewMockRunnable(ctrl)
	runnableB.EXPECT().Meta().Return(types.JobMeta{Name: "b"}).AnyTimes()
	runnableB.EXPECT().Run(gomock.Any()).
		Return(types.StepResults{{Name: "b", Status: types.StatusPass}}, nil).
		Times(1)

	g := gate.NewGate("Seed").
		Serial(
			"Phase",
			job.Adapt(runnableA),
			job.Adapt(runnableB),
		).Build()

	require.NoError(t, gate.RunGateNoRunner(context.Background(), g))
}
