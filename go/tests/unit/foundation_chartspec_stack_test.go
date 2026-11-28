package unit_test

import (
	"math"
	"testing"
	"testing/quick"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/chartspec"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

const (
	// stackTestRuns is how many random groups the normalize property checks.
	stackTestRuns = 500
	// stackTestTolerance is the float comparison slack.
	stackTestTolerance = 1e-9
	// stackTestOne is the span a normalized group fills.
	stackTestOne = 1.0
	// stackTestFirst is the first value of the worked group.
	stackTestFirst = 2.0
	// stackTestSecond is the second value of the worked group.
	stackTestSecond = 3.0
	// stackTestThird is the third value of the worked group.
	stackTestThird = 5.0
	// stackTestSum is the worked group's total.
	stackTestSum = 10.0
	// stackTestHalf halves a neutral band across zero.
	stackTestHalf = 0.5
	// stackTestKey is the colour value of a one-colour group.
	stackTestKey = "a"
	// stackTestScale keeps a generated value in a plain range.
	stackTestScale = 1000.0
)

// stackRows builds cells keyed by the given colour values, valued in order.
func stackRows(values []float64, keys ...string) []chartspec.Cell {
	out := make([]chartspec.Cell, 0, len(values))

	for i, v := range values {
		out = append(out, fixtures.Build(func(row *chartspec.Cell) {
			row.Key = []string{keys[i%len(keys)]}
			row.Value = v
		}))
	}

	return out
}

// stackPolarity is the sign table the diverging cases use.
func stackPolarity(value string) (int8, bool) {
	signs := map[string]int8{"bad": -1, "ok": 0, "good": 1}
	sign, found := signs[value]

	return sign, found
}

// TestChartspecStack_PlacesSegmentsByMode tests chartspec.StackCells.
//
// Why this test is important:
//   - A stacked bar is read by where each segment starts and stops; a segment
//     placed wrong turns a correct number into a misleading picture
//
// What it tests:
//   - zero stacking runs 0-2, 2-5, 5-10; normalize scales the same group to end
//     at 1; diverging puts the bad value below 0 and the good value above it and
//     splits the neutral one across 0; diverging with no polarity, an unknown
//     value, a negative value or an unknown mode is CodeInvalidInput; and a
//     500-group property check finds every normalized group ends at 1.
func TestChartspecStack_PlacesSegmentsByMode(t *testing.T) {
	t.Parallel()

	cells := stackRows(
		[]float64{stackTestFirst, stackTestSecond, stackTestThird},
		stackTestKey, "b", "c",
	)

	zero, err := chartspec.StackCells(cells, types.StackZero, nil)
	require.NoError(t, err)
	assert.InDelta(t, 0, zero[0].Start, 0)
	assert.InDelta(t, stackTestFirst, zero[0].End, 0)
	assert.InDelta(t, stackTestFirst, zero[1].Start, 0)
	assert.InDelta(t, stackTestSum, zero[2].End, 0)

	norm, err := chartspec.StackCells(cells, types.StackNormalize, nil)
	require.NoError(t, err)
	assert.InDelta(t, stackTestOne, norm[2].End, stackTestTolerance)

	mixed := stackRows(
		[]float64{stackTestFirst, stackTestSecond, stackTestThird},
		"bad", "ok", "good",
	)

	div, err := chartspec.StackCells(mixed, types.StackDiverging, stackPolarity)
	require.NoError(t, err)
	assert.InDelta(t, -stackTestSecond*stackTestHalf, div[0].Start, stackTestTolerance)
	assert.Less(t, div[0].End, div[0].Start)
	assert.InDelta(t, -stackTestSecond*stackTestHalf, div[1].Start, stackTestTolerance)
	assert.InDelta(t, stackTestSecond*stackTestHalf, div[2].Start, stackTestTolerance)
	assert.Greater(t, div[2].End, div[2].Start)

	_, err = chartspec.StackCells(mixed, types.StackDiverging, nil)
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput))

	_, err = chartspec.StackCells(cells, types.StackDiverging, stackPolarity)
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput))

	_, err = chartspec.StackCells(
		stackRows([]float64{-1}, stackTestKey), types.StackZero, nil,
	)
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput))

	_, err = chartspec.StackCells(cells, types.Stack("sideways"), nil)
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput))

	stackTestNormalizeProperty(t)
}

// stackTestNormalizeProperty checks that a normalized group ends at 1 (or stays at
// 0 when it totals 0) across random groups.
func stackTestNormalizeProperty(t *testing.T) {
	t.Helper()

	endsAtOne := func(raw []uint16) bool {
		values := make([]float64, 0, len(raw))
		for _, r := range raw {
			values = append(values, float64(r)/stackTestScale)
		}

		segments, sErr := chartspec.StackCells(
			stackRows(values, stackTestKey), types.StackNormalize, nil,
		)
		if sErr != nil {
			return false
		}

		if len(segments) == 0 {
			return true
		}

		end := segments[len(segments)-1].End

		return math.Abs(end-stackTestOne) <= stackTestTolerance || end == 0
	}

	require.NoError(t, quick.Check(endsAtOne, fixtures.BuildRef(func(row *quick.Config) {
		row.MaxCount = stackTestRuns
	})))
}
