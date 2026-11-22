package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/procedure"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// Names the when-skip naming test shares.
const (
	// whenSkipProc is the procedure under test.
	whenSkipProc = "when-skip"

	// whenSkipEnv is the value of the env variable the step names interpolate.
	whenSkipEnv = "staging"

	// whenSkipItems is the each list of the skipped each step.
	whenSkipItems = "pending_images"

	// whenSkipRows is the number of rows the procedure reports: one per step.
	whenSkipRows = 2
)

// TestServicesProcedure_WhenSkipRowNameIsInterpolated tests that a step skipped by its
// when guard reports its interpolated name.
//
// Why this test is important:
//   - A skipped step's row is what the operator reads; a row named `kube context "{env}"`
//     shows an unexpanded template where a run row would show the environment.
//   - A skipped each step whose name names its item has no item yet, and must still be
//     reported rather than fail the procedure.
//
// What it tests:
//   - A skipped step named `kube context "{env}"` with env=staging reports
//     `kube context "staging"` with status SKIP.
//   - A skipped each step named after its item keeps its template name, status SKIP,
//     and the run returns no error.
func TestServicesProcedure_WhenSkipRowNameIsInterpolated(t *testing.T) {
	t.Parallel()

	procs := []types.Procedure{fixtures.Build(func(row *types.Procedure) {
		row.Name = whenSkipProc
		row.Inputs = []string{"env", "cluster", whenSkipItems}
		row.Steps = []types.ProcStep{
			fixtures.Build(func(row *types.ProcStep) {
				row.Name = `kube context "{env}"`
				row.When = "cluster != ''"
				row.Fail = "unreachable"
			}),
			fixtures.Build(func(row *types.ProcStep) {
				row.Name = "pull {image}"
				row.When = "cluster != ''"
				row.Each = whenSkipItems
				row.As = "image"
				row.Fail = "unreachable"
			}),
		}
	})}

	eng := procEngine(t, new(recorder), procs, new(procedure.Deps))

	res, err := eng.Run(
		t.Context(),
		whenSkipProc,
		map[string]any{"env": whenSkipEnv, "cluster": "", whenSkipItems: []string{"a"}},
		stdx.Zero[types.RunOptions](),
	)
	require.NoError(t, err)
	require.Len(t, res, whenSkipRows)
	assert.Equal(t, `kube context "staging"`, res[0].Name)
	assert.Equal(t, types.StatusSkip, res[0].Status)
	assert.Equal(t, "pull {image}", res[1].Name)
	assert.Equal(t, types.StatusSkip, res[1].Status)
}
