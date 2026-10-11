package unit_test

import (
	"math"
	"math/rand"
	"slices"
	"testing"
	"testing/quick"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/chartspec"
)

// partialOf merges one single-observation partial per value (the per-fact rows a
// bucket holds) into one partial.
func partialOf(t *testing.T, values []float64) chartspec.Partial {
	t.Helper()
	var p chartspec.Partial
	for _, v := range values {
		one, err := chartspec.NewPartial(v)
		require.NoError(t, err)
		p.Merge(one)
	}
	return p
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
func sameAnswers(a, b chartspec.Partial) bool {
	for _, agg := range []types.Aggregate{
		types.AggCount, types.AggMin, types.AggMax, types.AggP50, types.AggP95, types.AggP99,
	} {
		x, y := chartspec.Finalize(a, agg), chartspec.Finalize(b, agg)
		if x != y && !(math.IsNaN(x) && math.IsNaN(y)) {
			return false
		}
	}
	for _, agg := range []types.Aggregate{types.AggSum, types.AggAvg} {
		x, y := chartspec.Finalize(a, agg), chartspec.Finalize(b, agg)
		if math.IsNaN(x) && math.IsNaN(y) {
			continue
		}
		if math.Abs(x-y) > 1e-9*math.Max(1, math.Abs(x)) {
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

	law := func(ra, rb, rc []float64) bool {
		a, b, c := partialOf(
			t,
			positives(ra),
		), partialOf(
			t,
			positives(rb),
		), partialOf(
			t,
			positives(rc),
		)
		before := chartspec.Finalize(b, types.AggP95)
		left := merged(merged(a, b), c)
		right := merged(a, merged(b, c))
		after := chartspec.Finalize(b, types.AggP95)
		unchanged := before == after || (math.IsNaN(before) && math.IsNaN(after))
		return sameAnswers(left, right) && sameAnswers(merged(a, b), merged(b, a)) &&
			unchanged
	}
	require.NoError(t, quick.Check(law, &quick.Config{MaxCount: 200}))

	a := partialOf(t, []float64{3, 1, 7})
	self := merged(a, a)
	assert.InDelta(t, 1.0, chartspec.Finalize(self, types.AggMin), 0)
	assert.InDelta(t, 7.0, chartspec.Finalize(self, types.AggMax), 0)
	assert.InDelta(t, 6.0, chartspec.Finalize(self, types.AggCount), 0)
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

	p := partialOf(t, []float64{1, 2, 3, 4})
	assert.InDelta(t, 10.0, chartspec.Finalize(p, types.AggSum), 0)
	assert.InDelta(t, 4.0, chartspec.Finalize(p, types.AggCount), 0)
	assert.InDelta(t, 2.5, chartspec.Finalize(p, types.AggAvg), 0)
	assert.InDelta(t, 1.0, chartspec.Finalize(p, types.AggMin), 0)
	assert.InDelta(t, 4.0, chartspec.Finalize(p, types.AggMax), 0)
	assert.InEpsilon(t, 2.0, chartspec.Finalize(p, types.AggP50), 0.01)

	var empty chartspec.Partial
	assert.InDelta(t, 0.0, chartspec.Finalize(empty, types.AggSum), 0)
	assert.InDelta(t, 0.0, chartspec.Finalize(empty, types.AggCount), 0)
	for _, agg := range []types.Aggregate{
		types.AggAvg, types.AggMin, types.AggMax, types.AggP95,
	} {
		assert.True(t, math.IsNaN(chartspec.Finalize(empty, agg)), agg)
	}
	assert.True(t, math.IsNaN(chartspec.Finalize(p, types.AggCountDistinct)))

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

	r := rand.New(rand.NewSource(7))
	for trial := range 50 {
		n := 50 + r.Intn(2000)
		values := make([]float64, n)
		for i := range values {
			values[i] = math.Exp(r.Float64()*14 - 7) // 1e-3 … 1e3, log-spread
		}
		shards := 1 + r.Intn(8)
		var total chartspec.Partial
		for s := range shards {
			var shard []float64
			for i := s; i < n; i += shards {
				shard = append(shard, values[i])
			}
			total.Merge(partialOf(t, shard))
		}
		sorted := slices.Clone(values)
		slices.Sort(sorted)
		for _, q := range []struct {
			agg types.Aggregate
			q   float64
		}{{types.AggP50, 0.5}, {types.AggP95, 0.95}, {types.AggP99, 0.99}} {
			exact := sorted[int(q.q*float64(n-1))]
			got := chartspec.Finalize(total, q.agg)
			assert.LessOrEqual(
				t,
				math.Abs(got-exact),
				0.01*exact+1e-12,
				"trial %d %s",
				trial,
				q.agg,
			)
		}
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
