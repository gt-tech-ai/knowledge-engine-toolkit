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

// TestGateRunLocal_RunsRunnableJobs tests that a gate composed of runner-less
// Runnables (lifted via job.Base.Runnable) runs to success through RunLocal without
// a CommandRunner.
//
// Why this test is important:
//   - Runner-less gates (e.g. DB seeding) compose Runnables; RunLocal must
//     execute them without threading a CommandRunner, so no consumer passes a nil
//     runner into the gate API
//
// What it tests:
//   - A Serial phase of Base.Runnable units runs: RunLocal returns nil and
//     every Runnable's Run executed
func TestGateRunLocal_RunsRunnableJobs(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)

	// Each Runnable's Run is expected exactly once — the gomock Times(1)
	// expectation (verified at ctrl.Finish) replaces the old boolean `ran` flag as
	// the proof that RunLocal executed both units.
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

	g := gate.New("Seed").
		Serial(
			"Phase",
			job.Base{}.Runnable(runnableA),
			job.Base{}.Runnable(runnableB),
		)

	require.NoError(t, g.RunLocal(context.Background()))
}
