package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/chartspec"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

const (
	// cellTestCube is the cube name the population count is asked about.
	cellTestCube = "cell_cube"
	// cellTestMin is the privacy floor the cases run under.
	cellTestMin = int64(5)
	// cellTestSmall is a cell count below the floor.
	cellTestSmall = int64(3)
	// cellTestMid is a cell count above the floor.
	cellTestMid = int64(10)
	// cellTestLarge is a second cell count above the floor.
	cellTestLarge = int64(30)
	// cellTestPopulation is a population above the floor.
	cellTestPopulation = int64(100)
	// cellTestSmallPopulation is a population below the floor.
	cellTestSmallPopulation = int64(4)
	// cellTestTolerance is the float comparison slack.
	cellTestTolerance = 1e-9
	// cellTestShareLow is the share a quarter of the visible total makes.
	cellTestShareLow = 0.25
	// cellTestShareHigh is the share three quarters of the visible total makes.
	cellTestShareHigh = 0.75
	// cellTestValueLow is the value of the cell backed by cellTestMid.
	cellTestValueLow = 10.0
	// cellTestValueHigh is the value of the cell backed by cellTestLarge.
	cellTestValueHigh = 30.0
	// cellTestValueSmall is the value of the cell backed by cellTestSmall.
	cellTestValueSmall = 3.0
)

// cellRow builds one cell with the given key, value and backing count.
func cellRow(key string, value float64, count int64) chartspec.Cell {
	return fixtures.Build(func(row *chartspec.Cell) {
		row.Key = []string{key}
		row.Value = value
		row.Count = count
	})
}

// cellRows is the three-cell input the cases share: one cell under the floor, two over.
func cellRows() []chartspec.Cell {
	return []chartspec.Cell{
		cellRow("a", cellTestValueSmall, cellTestSmall),
		cellRow("b", cellTestValueLow, cellTestMid),
		cellRow("c", cellTestValueHigh, cellTestLarge),
	}
}

// cellPolicy builds a MinCellPolicy over the mock with the shared floor.
func cellPolicy(counts interfaces.CountSource) chartspec.MinCellPolicy {
	return fixtures.Build(func(row *chartspec.MinCellPolicy) {
		row.Count = counts
		row.Min = cellTestMin
	})
}

// TestChartspecCell_MinCellSuppressesSmallCells tests chartspec.ApplyMinCell.
//
// Why this test is important:
//   - A cell backed by a handful of observations can single out the people behind
//     it; the floor is the privacy promise a chart makes, so a small cell must
//     never carry its value out
//
// What it tests:
//   - with counts 3, 10 and 30 under a floor of 5 the 3-cell comes back suppressed
//     with value 0 while the others keep their values; a population of 4, below
//     the floor, is refused as CodeFailedPrecondition; a floor of 5 with no count
//     source is CodeInvalidInput; and a floor of zero leaves every cell visible
//     without asking for the population.
func TestChartspecCell_MinCellSuppressesSmallCells(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	counts := mocks.NewMockCountSource(ctrl)
	counts.EXPECT().
		Count(gomock.Any(), cellTestCube, nil).
		Return(cellTestPopulation, nil)

	got, err := chartspec.ApplyMinCell(
		context.Background(), cellRows(), cellPolicy(counts), cellTestCube, nil,
	)
	require.NoError(t, err)
	require.Len(t, got, len(cellRows()))
	assert.True(t, got[0].Suppressed)
	assert.InDelta(t, 0, got[0].Value, 0)
	assert.False(t, got[1].Suppressed)
	assert.InDelta(t, cellTestValueLow, got[1].Value, 0)
	assert.False(t, got[2].Suppressed)

	small := mocks.NewMockCountSource(ctrl)
	small.EXPECT().
		Count(gomock.Any(), cellTestCube, nil).
		Return(cellTestSmallPopulation, nil)

	_, err = chartspec.ApplyMinCell(
		context.Background(), cellRows(), cellPolicy(small), cellTestCube, nil,
	)
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeFailedPrecondition))

	_, err = chartspec.ApplyMinCell(
		context.Background(), cellRows(), cellPolicy(nil), cellTestCube, nil,
	)
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput))

	off := fixtures.Build(func(row *chartspec.MinCellPolicy) {
		row.Count = nil
		row.Min = 0
	})

	free, err := chartspec.ApplyMinCell(
		context.Background(), cellRows(), off, cellTestCube, nil,
	)
	require.NoError(t, err)
	assert.False(t, free[0].Suppressed)
}

// TestChartspecCell_PercentIgnoresSuppressed tests chartspec.PercentOfTotal.
//
// Why this test is important:
//   - A share is only honest against the cells a reader can see; counting a hidden
//     cell in the denominator would let a reader back out its value from the others
//
// What it tests:
//   - suppressing the 3-cell and taking percent of total over values 10 and 30
//     gives 0.25 and 0.75 and leaves the suppressed cell at 0; an all-zero input
//     stays all zero instead of dividing by zero.
func TestChartspecCell_PercentIgnoresSuppressed(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	counts := mocks.NewMockCountSource(ctrl)
	counts.EXPECT().
		Count(gomock.Any(), cellTestCube, nil).
		Return(cellTestPopulation, nil)

	hidden, err := chartspec.ApplyMinCell(
		context.Background(), cellRows(), cellPolicy(counts), cellTestCube, nil,
	)
	require.NoError(t, err)

	shares := chartspec.PercentOfTotal(hidden)
	require.Len(t, shares, len(hidden))
	assert.InDelta(t, 0, shares[0].Value, 0)
	assert.InDelta(t, cellTestShareLow, shares[1].Value, cellTestTolerance)
	assert.InDelta(t, cellTestShareHigh, shares[2].Value, cellTestTolerance)

	zero := chartspec.PercentOfTotal([]chartspec.Cell{cellRow("z", 0, cellTestMid)})
	assert.InDelta(t, 0, zero[0].Value, 0)
}
