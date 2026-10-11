package unit_test

import (
	"math/rand"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/quick"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/chartspec"
)

// randShelf is a randomly generated shelf expression for the algebra property tests.
type randShelf struct {
	// E is the generated expression.
	E *types.AlgebraExpr
}

// shelfLeaves returns the field references random shelves draw from.
func shelfLeaves() []types.FieldRef {
	return []types.FieldRef{
		{Name: "team"},
		{Name: "model"},
		{Name: "workspace"},
		{Name: "tokens_in", Agg: types.AggSum},
	}
}

// genShelf builds a random expression at most depth levels deep.
func genShelf(r *rand.Rand, depth int) *types.AlgebraExpr {
	if depth <= 1 || r.Intn(3) == 0 {
		leaf := shelfLeaves()[r.Intn(len(shelfLeaves()))]
		return &types.AlgebraExpr{Field: &leaf}
	}
	opsList := []types.AlgebraOp{
		types.AlgebraCross,
		types.AlgebraConcat,
		types.AlgebraNest,
	}
	n := 1 + r.Intn(3)
	args := make([]*types.AlgebraExpr, n)
	for i := range args {
		args[i] = genShelf(r, depth-1)
	}
	return &types.AlgebraExpr{Op: opsList[r.Intn(len(opsList))], Args: args}
}

// Generate implements quick.Generator.
func (randShelf) Generate(r *rand.Rand, _ int) reflect.Value {
	return reflect.ValueOf(randShelf{E: genShelf(r, 3)})
}

// tupleKeys renders tuples as sorted "field,field" strings (a multiset), ignoring Nested.
func tupleKeys(tuples []chartspec.Tuple) []string {
	out := make([]string, 0, len(tuples))
	for _, t := range tuples {
		names := make([]string, 0, len(t.Fields))
		for _, f := range t.Fields {
			names = append(names, f.Key())
		}
		out = append(out, strings.Join(names, ","))
	}
	slices.Sort(out)
	return out
}

// op builds an operator node over args.
func op(o types.AlgebraOp, args ...*types.AlgebraExpr) *types.AlgebraExpr {
	return &types.AlgebraExpr{Op: o, Args: args}
}

// TestAlgebra_CrossDistributesOverConcat tests the table-algebra law
// a × (b + c) = (a × b) + (a × c) over random shelves.
//
// Why this test is important:
//   - The pane table is derived from the normalized algebra; if the normal form
//     broke the distributive law, two equivalent shelf layouts would draw
//     different charts
//
// What it tests:
//   - for random a, b, c the tuple multisets of both sides are equal
//   - a hand-checked case: team × (model + workspace) = [team,model; team,workspace]
func TestAlgebra_CrossDistributesOverConcat(t *testing.T) {
	t.Parallel()

	law := func(a, b, c randShelf) bool {
		left := chartspec.Normalize(
			op(types.AlgebraCross, a.E, op(types.AlgebraConcat, b.E, c.E)),
		)
		right := chartspec.Normalize(
			op(
				types.AlgebraConcat,
				op(types.AlgebraCross, a.E, b.E),
				op(types.AlgebraCross, a.E, c.E),
			),
		)
		return slices.Equal(tupleKeys(left), tupleKeys(right))
	}
	require.NoError(t, quick.Check(law, &quick.Config{MaxCount: 300}))

	team, model, ws := &types.AlgebraExpr{
		Field: &types.FieldRef{Name: "team"},
	}, &types.AlgebraExpr{
		Field: &types.FieldRef{Name: "model"},
	}, &types.AlgebraExpr{
		Field: &types.FieldRef{Name: "workspace"},
	}
	assert.Equal(
		t,
		[]string{"team,model", "team,workspace"},
		tupleKeys(
			chartspec.Normalize(
				op(types.AlgebraCross, team, op(types.AlgebraConcat, model, ws)),
			),
		),
	)
	assert.Equal(
		t,
		[]chartspec.Tuple{{}},
		chartspec.Normalize(nil),
		"an empty shelf is the cross identity",
	)
}

// TestAlgebra_NestIsSubsetOfCross tests that nesting yields the cross product's
// tuples, flagged so the reducer keeps only observed combinations.
//
// Why this test is important:
//   - Nest must never invent a combination cross would not produce, and the reducer
//     relies on the flag to drop unobserved ones
//
// What it tests:
//   - for random a, b, every tuple of a / b is a tuple of a × b, and is flagged Nested
//   - a cross with no nest inside produces no Nested tuple
func TestAlgebra_NestIsSubsetOfCross(t *testing.T) {
	t.Parallel()

	law := func(a, b randShelf) bool {
		nest := chartspec.Normalize(op(types.AlgebraNest, a.E, b.E))
		cross := tupleKeys(chartspec.Normalize(op(types.AlgebraCross, a.E, b.E)))
		for _, tuple := range nest {
			if !tuple.Nested {
				return false
			}
			if _, found := slices.BinarySearch(
				cross,
				tupleKeys([]chartspec.Tuple{tuple})[0],
			); !found {
				return false
			}
		}
		return true
	}
	require.NoError(t, quick.Check(law, &quick.Config{MaxCount: 300}))

	team, model := &types.AlgebraExpr{
		Field: &types.FieldRef{Name: "team"},
	}, &types.AlgebraExpr{
		Field: &types.FieldRef{Name: "model"},
	}
	for _, tuple := range chartspec.Normalize(op(types.AlgebraCross, team, model)) {
		assert.False(t, tuple.Nested)
	}
}
