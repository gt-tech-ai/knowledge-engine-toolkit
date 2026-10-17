package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/gate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/job"
)

// TestGateRunLocal_RunsLocalJobs tests that a gate composed of runner-less
// Runnables (lifted via job.Base.Runnable) runs to success through RunLocal without
// a CommandRunner.
//
// Why this test is important:
//   - Runner-less gates (e.g. DB seeding) compose Runnables; RunLocal must
//     execute them without threading a CommandRunner, so no consumer passes a nil
//     runner into the gate API
//
// What it tests:
//   - A Serial phase of Base.Local jobs runs: RunLocal returns nil and every job's
//     work ran, in order.
func TestGateRunLocal_RunsLocalJobs(t *testing.T) {
	t.Parallel()

	var ran []string
	local := func(name string) func(context.Context) (types.StepResults, error) {
		return func(context.Context) (types.StepResults, error) {
			ran = append(ran, name)
			return types.StepResults{{Name: name, Status: types.StatusPass}}, nil
		}
	}
	g := gate.New("Seed").
		Serial(
			"Phase",
			job.Base{}.Local("a", "Phase", local("a")),
			job.Base{}.Local("b", "Phase", local("b")),
		)

	require.NoError(t, g.RunLocal(context.Background()))
	assert.Equal(t, []string{"a", "b"}, ran)
}
