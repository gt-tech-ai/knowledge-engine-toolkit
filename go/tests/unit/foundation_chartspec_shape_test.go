package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/chartspec"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

const (
	// shapeTestMin is the privacy floor a reduced spec asks for.
	shapeTestMin = int64(5)
	// shapeTestPopulation is a population above that floor.
	shapeTestPopulation = int64(100)
	// shapeTestTiny is a population below that floor.
	shapeTestTiny = int64(4)
	// shapeTestTolerance is the float comparison slack.
	shapeTestTolerance = 1e-9
	// shapeTestQuarter is the share of the visible total the smaller cell holds.
	shapeTestQuarter = 0.25
	// shapeTestWhole is the span a normalized group fills.
	shapeTestWhole = 1.0
	// shapeTestZero is the line a diverging stack grows away from.
	shapeTestZero = 0.0
	// shapeTestPair is the number of cells in the diverging case.
	shapeTestPair = 2
)

// shapeReduced is a stacked spec asking for percent of total over a floor of 5.
func shapeReduced(t *testing.T, mode types.Stack) chartspec.Plan {
	t.Helper()

	spec := shapeSpec(mode)

	spec.Reduce = &types.Reduce{Percent: types.PercentOfTotal, MinCell: shapeTestMin}

	plan, err := chartspec.Compile(&spec, shapeCube(shapeSigns()))
	require.NoError(t, err)

	return plan
}

// shapeCounts is a count source answering one population.
func shapeCounts(t *testing.T, population int64) *mocks.MockCountSource {
	t.Helper()

	counts := mocks.NewMockCountSource(gomock.NewController(t))
	counts.EXPECT().
		Count(gomock.Any(), textGenaiCalls, nil).
		Return(population, nil).
		AnyTimes()

	return counts
}

// shapeCells is the three-cell input of the worked example, keyed by sentiment.
func shapeCells() []chartspec.Cell {
	return []chartspec.Cell{
		cellRow(shapeNegative, cellTestValueSmall, cellTestSmall),
		cellRow(shapeNeutral, cellTestValueLow, cellTestMid),
		cellRow(shapePositive, cellTestValueHigh, cellTestLarge),
	}
}

// TestChartspecShape_AppliesFloorPercentThenStack tests chartspec.Plan.Shape.
//
// Why this test is important:
//   - The three steps only mean something in order: hide the small cells, take
//     shares of what remains, then lay the shares out; any other order leaks a
//     hidden cell into a share or draws a segment for a value that was removed
//
// What it tests:
//   - counts 3, 10 and 30 under min_cell 5 with percent of total suppress the
//     3-cell and give shares 0.25 and 0.75, stacked end to end
//   - a normalized stack of the shares ends at 1
//   - a diverging stack of a negative and a positive cell puts the negative below 0
//     and the positive above it
//   - a population below the floor is CodeFailedPrecondition, and a floor with no
//     count source is CodeInvalidInput
func TestChartspecShape_AppliesFloorPercentThenStack(t *testing.T) {
	t.Parallel()

	plan := shapeReduced(t, types.StackZero)
	counts := shapeCounts(t, shapeTestPopulation)

	shaped, err := plan.Shape(context.Background(), shapeCells(), counts)
	require.NoError(t, err)
	require.Len(t, shaped.Cells, len(shapeCells()))
	require.Len(t, shaped.Segments, len(shapeCells()))
	assert.True(t, shaped.Cells[0].Suppressed)
	assert.InDelta(t, shapeTestQuarter, shaped.Cells[1].Value, shapeTestTolerance)
	assert.InDelta(t, shapeTestQuarter, shaped.Segments[1].End, shapeTestTolerance)
	assert.InDelta(t, shapeTestWhole, shaped.Segments[2].End, shapeTestTolerance)

	normal := shapeReduced(t, types.StackNormalize)

	shaped, err = normal.Shape(context.Background(), shapeCells(), counts)
	require.NoError(t, err)
	assert.InDelta(t, shapeTestWhole, shaped.Segments[2].End, shapeTestTolerance)

	diverging := shapeReduced(t, types.StackDiverging)
	pair := shapeCells()[:shapeTestPair]

	pair[1] = shapeCells()[shapeTestPair]
	pair[0].Count = cellTestLarge

	shaped, err = diverging.Shape(context.Background(), pair, counts)
	require.NoError(t, err)
	assert.Less(t, shaped.Segments[0].End, shapeTestZero)
	assert.Greater(t, shaped.Segments[1].End, shapeTestZero)

	_, err = plan.Shape(
		context.Background(), shapeCells(), shapeCounts(t, shapeTestTiny),
	)
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeFailedPrecondition))

	_, err = plan.Shape(context.Background(), shapeCells(), nil)
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput))
}
