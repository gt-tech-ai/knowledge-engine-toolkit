package unit_test

import (
	"math"
	"slices"
	"testing"
	"testing/quick"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/chartspec"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// partialOf merges one single-observation partial per value (the per-fact rows a
// bucket holds) into one partial.
func partialOf(t *testing.T, values []float64) chartspec.Partial {
	t.Helper()

	var partial chartspec.Partial

	for _, v := range values {
		one, err := chartspec.NewPartial(v)
		require.NoError(t, err)
		partial.Merge(one)
	}

	return partial
}

// positives maps raw quick-generated floats into observation-like values in [0.001, 1e6).
func positives(raw []float64) []float64 {
	out := make([]float64, 0, len(raw))
	for _, v := range raw {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}

		out = append(out, 0.001+math.Mod(math.Abs(v), 1e6))
	}

	return out
}

// sameAnswers reports whether two partials finalize to the same answer for every
// aggregate: count/min/max exactly, sum/avg within float rounding, quantiles exactly
// (sketch merges add bin counts, which is exact).
func sameAnswers(currentPartial, partial chartspec.Partial) bool {
	for _, agg := range []types.Aggregate{
		types.AggCount, types.AggMin, types.AggMax, types.AggP50, types.AggP95, types.AggP99,
	} {
		x, y := chartspec.Finalize(currentPartial, agg), chartspec.Finalize(partial, agg)
		if x != y && !(math.IsNaN(x) && math.IsNaN(y)) {
			return false
		}
	}

	return sameAnswersFinalized(currentPartial, partial)
}

// sameAnswersFinalized continues sameAnswers: finalized.
func sameAnswersFinalized(a, b chartspec.Partial) bool {
	for _, agg := range []types.Aggregate{types.AggSum, types.AggAvg} {
		x, y := chartspec.Finalize(a, agg), chartspec.Finalize(b, agg)
		if math.IsNaN(x) && math.IsNaN(y) {
			continue
		}

		if math.Abs(x-y) > wantAbs*math.Max(1, math.Abs(x)) {
			return false
		}
	}

	return true
}

// merged returns a fresh partial equal to x ⊕ y, leaving both operands untouched.
func merged(x, y chartspec.Partial) chartspec.Partial {
	var out chartspec.Partial

	out.Merge(x)
	out.Merge(y)

	return out
}

// TestPartial_MergeIsAssociativeAndCommutative tests the reducer's merge laws on
// random observation sets.
//
// Why this test is important:
//   - Buckets are fanned out and merged in whatever order they return; if merge
//     were order-sensitive the same query would answer differently run to run
//
// What it tests:
//   - (a⊕b)⊕c and a⊕(b⊕c) finalize to the same answer for every aggregate
//   - a⊕b and b⊕a finalize identically
//   - merging a partial with itself keeps min and max (idempotent extrema) and
//     doubles count; merging does not mutate the right operand
func TestPartial_MergeIsAssociativeAndCommutative(t *testing.T) {
	t.Parallel()

	law := func(ra, currentAmounts, amounts []float64) bool {
		secondPartial, partial, currentPartial := partialOf(
			t,
			positives(ra),
		), partialOf(
			t,
			positives(currentAmounts),
		), partialOf(
			t,
			positives(amounts),
		)
		before := chartspec.Finalize(partial, types.AggP95)
		left := merged(merged(secondPartial, partial), currentPartial)
		right := merged(secondPartial, merged(partial, currentPartial))
		after := chartspec.Finalize(partial, types.AggP95)
		unchanged := before == after || (math.IsNaN(before) && math.IsNaN(after))

		return sameAnswers(left, right) &&
			sameAnswers(merged(secondPartial, partial), merged(partial, secondPartial)) &&
			unchanged
	}
	require.NoError(
		t,
		quick.Check(
			law,
			fixtures.BuildRef(func(row *quick.Config) {
				row.MaxCount = 200
			}),
		),
	)

	a := partialOf(t, []float64{3, 1, 7})
	self := merged(a, a)
	assert.InDelta(t, wantFinalize, chartspec.Finalize(self, types.AggMin), 0)
	assert.InDelta(t, wantFinalizeCommutative, chartspec.Finalize(self, types.AggMax), 0)
	assert.InDelta(
		t,
		wantFinalizeAndCommutative,
		chartspec.Finalize(self, types.AggCount),
		0,
	)
}

// TestFinalize_ComputesEachAggregate tests the value each aggregate finalizes to.
//
// Why this test is important:
//   - Finalize is the last step before a number reaches a panel
//
// What it tests:
//   - {1, 2, 3, 4} → sum 10, count 4, avg 2.5, min 1, max 4, p50 within 1% of 2
//   - an empty partial → sum 0, count 0, avg/min/max/p95 NaN; count_distinct is NaN
func TestFinalize_ComputesEachAggregate(t *testing.T) {
	t.Parallel()

	partial := partialOf(t, []float64{1, 2, 3, 4})
	assert.InDelta(t, wantFinalizeAggregate, chartspec.Finalize(partial, types.AggSum), 0)
	assert.InDelta(
		t,
		wantFinalizeEachAggregate,
		chartspec.Finalize(partial, types.AggCount),
		0,
	)
	assert.InDelta(
		t,
		wantFinalizeComputesEachAggregate,
		chartspec.Finalize(partial, types.AggAvg),
		0,
	)
	assert.InDelta(t, wantFinalize, chartspec.Finalize(partial, types.AggMin), 0)
	assert.InDelta(
		t,
		wantFinalizeEachAggregate,
		chartspec.Finalize(partial, types.AggMax),
		0,
	)
	assert.InEpsilon(
		t,
		inEpsilonInput,
		chartspec.Finalize(partial, types.AggP50),
		inEpsilonInputAggregate,
	)

	var empty chartspec.Partial

	assert.InDelta(
		t,
		wantFinalizeZeroCount,
		chartspec.Finalize(empty, types.AggSum),
		0,
	)
	assert.InDelta(
		t,
		wantFinalizeZeroCount,
		chartspec.Finalize(empty, types.AggCount),
		0,
	)

	for _, agg := range []types.Aggregate{
		types.AggAvg, types.AggMin, types.AggMax, types.AggP95,
	} {
		assert.True(t, math.IsNaN(chartspec.Finalize(empty, agg)), agg)
	}

	assert.True(t, math.IsNaN(chartspec.Finalize(partial, types.AggCountDistinct)))

	_, err := chartspec.NewPartial(math.NaN())
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// TestSketch_MergePreservesQuantilesWithin1Pct tests that quantiles read from
// merged per-shard sketches stay within DDSketch's 1% relative accuracy.
//
// Why this test is important:
//   - p95 latency is read from sketches merged across buckets and partitions; a
//     merge that lost accuracy would misreport the SLO the panels show
//
// What it tests:
//   - for random positive values split across random shards, p50/p95/p99 of the
//     merged sketch are within 1% of the exact value at rank q·(n−1)
func TestSketch_MergePreservesQuantilesWithin1Pct(t *testing.T) {
	t.Parallel()

	r := fixtures.NewSequence(newSequenceInput)

	testSketchMergePreservesQuantilesWithin1PctRunsTrials(t, r)
}

// mergedShards splits values round-robin into the given number of shards and merges
// each shard's partial into one.
func mergedShards(t *testing.T, values []float64, shards int) chartspec.Partial {
	t.Helper()

	var total chartspec.Partial

	for s := range shards {
		var shard []float64

		for i := s; i < len(values); i += shards {
			shard = append(shard, values[i])
		}

		total.Merge(partialOf(t, shard))
	}

	return total
}

// assertQuantilesWithin1Pct checks each sketched quantile of total against the exact
// quantile of values, within 1% relative error.
func assertQuantilesWithin1Pct(
	t *testing.T,
	total chartspec.Partial,
	values []float64,
	trial int,
) {
	t.Helper()

	sorted := slices.Sorted(slices.Values(values))

	for _, item := range []struct {
		agg types.Aggregate
		q   float64
	}{{types.AggP50, medianQuantile}, {types.AggP95, iterations0Pct}, {
		types.AggP99,
		iterations0Within1Pct,
	}} {
		exact := sorted[int(item.q*float64(len(values)-1))]
		got := chartspec.Finalize(total, item.agg)
		assert.LessOrEqual(
			t,
			math.Abs(got-exact),
			0.01*exact+1e-12,
			"trial %d %s",
			trial,
			item.agg,
		)
	}
}

// testSketchMergePreservesQuantilesWithin1PctRunsTrials continues
// testSketchMergePreservesQuantilesWithin1Pct: runs trials.
func testSketchMergePreservesQuantilesWithin1PctRunsTrials(
	t *testing.T,
	sequence *fixtures.Sequence,
) {
	t.Helper()

	for trial := range iterationsTrials {
		n := initialN + sequence.IntN(upperBoundTrials)
		values := make([]float64, n)

		for i := range values {
			values[i] = math.Exp(sequence.Float64()*14 - 7) // 1e-3 … 1e3, log-spread
		}

		shards := 1 + sequence.IntN(upperBoundRunsTrials)

		total := mergedShards(t, values, shards)

		assertQuantilesWithin1Pct(t, total, values, trial)
	}
}

// FuzzChartSpecParse fuzzes the ChartSpec JSON parser.
//
// Why this test is important:
//   - The parser takes browser-supplied JSON; a panic would crash the analytics
//     service and an uncoded error would surface as a 500
//
// What it tests:
//   - for any input, Parse never panics and every rejection is CodeInvalidInput
func FuzzChartSpecParse(f *testing.F) {
	f.Add([]byte(validSpecJSON))
	f.Add([]byte(`{"cube":"genai_calls","rows":` + nested(9) + `}`))
	f.Add([]byte(`{"cube":"genai_calls","filter":{"$and":[{"$eq":{"team":"a"}}]}}`))
	f.Add([]byte(`{"cube":"genai_calls","rows":{"op":"nest","args":[null]}}`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, err := chartspec.Parse(b, chartCubeMap())
		if err != nil && apperr.Code(err) != apperr.CodeInvalidInput {
			t.Fatalf("uncoded rejection %v for %q", err, b)
		}
	})
}

const (
	// inEpsilonInput is the in epsilon input the tests use.
	inEpsilonInput = 2.0

	// inEpsilonInputAggregate is the in epsilon input aggregate the tests use.
	inEpsilonInputAggregate = 0.01

	// initialN is the initial n the tests use.
	initialN = 50

	// medianQuantile is the medianQuantile the tests use.
	medianQuantile = 0.5

	// iterations0Pct is the medianQuantile pct the tests use.
	iterations0Pct = 0.95

	// iterations0Within1Pct is the medianQuantile within1 pct the tests use.
	iterations0Within1Pct = 0.99

	// iterationsTrials is the iterations trials the tests use.
	iterationsTrials = 50

	// newSequenceInput is the new sequence input the tests use.
	newSequenceInput = 7

	// upperBoundRunsTrials is the upper bound runs trials the tests use.
	upperBoundRunsTrials = 8

	// upperBoundTrials is the upper bound trials the tests use.
	upperBoundTrials = 2000

	// wantAbs is the expected abs.
	wantAbs = 1e-9

	// wantFinalize is the expected finalize.
	wantFinalize = 1.0

	// wantFinalizeAggregate is the expected finalize aggregate.
	wantFinalizeAggregate = 10.0

	// wantFinalizeAndCommutative is the expected finalize and commutative.
	wantFinalizeAndCommutative = 6.0

	// wantFinalizeCommutative is the expected finalize commutative.
	wantFinalizeCommutative = 7.0

	// wantFinalizeComputesEachAggregate is the expected finalize computes each aggregate.
	wantFinalizeComputesEachAggregate = 2.5

	// wantFinalizeEachAggregate is the expected finalize each aggregate.
	wantFinalizeEachAggregate = 4.0

	// wantFinalizeZeroCount is the expected finalize of an empty aggregate.
	wantFinalizeZeroCount = 0.0
)
