package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// TestStepBuilders tests the single-step result builders.
//
// Why this test is important:
//   - Every check and tool step reports through these builders; a check that dropped
//     its error, or turned a failure into a pass, would let a broken gate go green.
//
// What it tests:
//   - NewStep keeps the status and records the error's text (empty for nil); DetailStep
//     keeps the status and carries its detail text, with no error; OneStep wraps NewStep
//     as one result; CheckStep is a pass with a nil error, and a failure that
//     carries and returns the very same error otherwise.
func TestStepBuilders(t *testing.T) {
	t.Parallel()

	boom := apperr.New(apperr.CodeUpstream, textBoom)
	assert.Equal(t,
		types.StepResult{Name: textX, Status: types.StatusWarn, Error: boom.Error()},
		types.NewStep(textX, types.StatusWarn, boom))
	assert.Empty(t, types.NewStep(textX, types.StatusPass, nil).Error)
	assert.Equal(
		t,
		types.StepResult{
			Name:   textDLower,
			Status: types.StatusWarn,
			Detail: []byte("a\nb"),
		},
		types.DetailStep(textDLower, types.StatusWarn, "a\nb"),
	)
	assert.Equal(t,
		types.StepResults{{Name: "y", Status: types.StatusSkip}},
		types.OneStep("y", types.StatusSkip, nil))

	results, err := types.CheckStep(textLint, nil)
	require.NoError(t, err)
	assert.Equal(
		t,
		types.StepResults{{Name: textLint, Status: types.StatusPass}},
		results,
	)

	results, err = types.CheckStep(textLint, boom)
	assert.Same(t, boom, err)
	assert.Equal(
		t,
		types.StepResults{
			{Name: textLint, Status: types.StatusFail, Error: boom.Error()},
		},
		results,
	)
}
