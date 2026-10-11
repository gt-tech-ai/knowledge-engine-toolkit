package chartspec

import (
	"math"

	"github.com/DataDog/sketches-go/ddsketch"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// SketchRelativeAccuracy is the relative accuracy of every quantile sketch: a
// p50/p95/p99 is within 1% of the exact value at its rank.
const SketchRelativeAccuracy = 0.01

// Partial is the mergeable summary of a set of observations of one measure: the
// additive sum, count, min and max, and a DDSketch for quantiles. The zero value
// is the empty summary (Count 0, no sketch). Merging is associative and
// commutative, so buckets can be reduced in any order.
type Partial struct {
	// Sketch is the quantile sketch (nil while empty).
	Sketch *ddsketch.DDSketch
	// Sum is the sum of the observations.
	Sum float64
	// Count is the number of observations.
	Count float64
	// Min is the smallest observation (meaningless while Count is 0).
	Min float64
	// Max is the largest observation (meaningless while Count is 0).
	Max float64
}

// NewPartial returns the summary of one observation. A NaN or infinite value is
// CodeInvalidInput.
func NewPartial(value float64) (Partial, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return Partial{}, apperr.New(
			apperr.CodeInvalidInput,
			"chartspec: observation must be finite",
		)
	}
	sketch, err := ddsketch.NewDefaultDDSketch(SketchRelativeAccuracy)
	if err != nil {
		return Partial{}, apperr.Wrap(
			err,
			apperr.CodeInternal,
			"chartspec: create sketch",
		)
	}
	if err := sketch.Add(value); err != nil {
		return Partial{}, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"chartspec: add observation",
		)
	}
	return Partial{Sketch: sketch, Sum: value, Count: 1, Min: value, Max: value}, nil
}

// Merge folds o into p. o is not modified and p never shares o's sketch.
func (p *Partial) Merge(o Partial) {
	if o.Count == 0 {
		return
	}
	if p.Count == 0 {
		*p = o
		if o.Sketch != nil {
			p.Sketch = o.Sketch.Copy()
		}
		return
	}
	p.Sum += o.Sum
	p.Count += o.Count
	p.Min = math.Min(p.Min, o.Min)
	p.Max = math.Max(p.Max, o.Max)
	switch {
	case o.Sketch == nil:
	case p.Sketch == nil:
		p.Sketch = o.Sketch.Copy()
	default:
		mergeSketch(p.Sketch, o.Sketch)
	}
}

// mergeSketch adds src's bins into dst. Every sketch here shares one index
// mapping, so MergeWith succeeds; a sketch decoded with a different mapping is
// re-binned value by value instead, which keeps the relative-accuracy bound.
func mergeSketch(dst, src *ddsketch.DDSketch) {
	if err := dst.MergeWith(src); err == nil {
		return
	}
	src.ForEach(func(value, count float64) bool {
		_ = dst.AddWithCount(
			value,
			count,
		) // value came from a valid sketch bin; Add cannot reject it
		return false
	})
}

// Finalize computes agg from p: sum and count (0 when empty), avg, min, max, and
// p50/p95/p99 from the sketch. An empty summary yields NaN for the
// value-dependent aggregates; count_distinct, which additive partials cannot
// answer, yields NaN.
func Finalize(p Partial, agg types.Aggregate) float64 {
	switch agg {
	case types.AggSum:
		return p.Sum
	case types.AggCount:
		return p.Count
	case types.AggNone, types.AggCountDistinct:
		return math.NaN()
	case types.AggAvg,
		types.AggMin,
		types.AggMax,
		types.AggP50,
		types.AggP95,
		types.AggP99:
		return finalizeValue(p, agg)
	default:
		return math.NaN()
	}
}

// finalizeValue finalizes a value-dependent aggregate, NaN for an empty summary.
func finalizeValue(p Partial, agg types.Aggregate) float64 {
	if p.Count == 0 {
		return math.NaN()
	}
	switch agg {
	case types.AggAvg:
		return p.Sum / p.Count
	case types.AggMin:
		return p.Min
	case types.AggMax:
		return p.Max
	case types.AggP50:
		return quantile(p.Sketch, 0.5)
	case types.AggP95:
		return quantile(p.Sketch, 0.95)
	case types.AggP99:
		return quantile(p.Sketch, 0.99)
	case types.AggNone, types.AggSum, types.AggCount, types.AggCountDistinct:
		return math.NaN()
	default:
		return math.NaN()
	}
}

// quantile reads q from s, NaN when the sketch is missing or empty.
func quantile(s *ddsketch.DDSketch, q float64) float64 {
	if s == nil {
		return math.NaN()
	}
	v, err := s.GetValueAtQuantile(q)
	if err != nil {
		return math.NaN()
	}
	return v
}
