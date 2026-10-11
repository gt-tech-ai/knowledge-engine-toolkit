package unit_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/chartspec"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
)

// chartCubeMap is the allow-list of the genai_calls test cube: three dimensions, a
// time field and two measures with their permitted aggregates.
func chartCubeMap() *listquery.Map {
	return listquery.NewMap().Add(
		listquery.String("team"),
		listquery.String("workspace"),
		listquery.String("model"),
		listquery.Time("ts").AsTime(),
		listquery.Float("tokens_in").AsMeasure(types.AggSum, types.AggAvg, types.AggMax),
		listquery.Float("duration_s").AsMeasure(types.AggAvg, types.AggP95),
	)
}

// validSpecJSON is a spec exercising every wire member.
const validSpecJSON = `{
  "cube": "genai_calls",
  "rows": {"op": "cross", "args": [{"field": "team"}, {"field": "model"}]},
  "columns": {"field": "tokens_in", "agg": "sum"},
  "detail": [{"field": "workspace"}],
  "encodings": {"color": {"field": "model"}},
  "filter": {"$eq": {"team": "t-1"}},
  "sort": [{"field": "sum(tokens_in)", "desc": true}],
  "time_range": {"from": "2026-10-01T00:00:00Z", "to": "2026-10-09T00:00:00Z"},
  "grain": "day",
  "mark": "bar"
}`

// TestChartSpecParse_AcceptsValidSpec tests that a well-formed spec parses into the
// typed ChartSpec and re-serializes to equivalent JSON.
//
// Why this test is important:
//   - The browser, the API bridge and the analytics service exchange this JSON; a
//     member dropped on the way through would silently change the chart
//
// What it tests:
//   - every member is decoded (shelf tree, detail, encodings, sort, range, grain, mark)
//   - the filter is compiled into a listquery FilterClause on "team"
//   - marshalling the parsed spec and parsing it again yields the same spec
func TestChartSpecParse_AcceptsValidSpec(t *testing.T) {
	t.Parallel()

	spec, err := chartspec.Parse([]byte(validSpecJSON), chartCubeMap())
	require.NoError(t, err)

	assert.Equal(t, "genai_calls", spec.Cube)
	assert.Equal(t, types.AlgebraCross, spec.Rows.Op)
	assert.Equal(t, &types.FieldRef{Name: "team"}, spec.Rows.Args[0].Field)
	assert.Equal(
		t,
		&types.FieldRef{Name: "tokens_in", Agg: types.AggSum},
		spec.Columns.Field,
	)
	assert.Equal(t, []types.FieldRef{{Name: "workspace"}}, spec.Detail)
	assert.Equal(t, &types.FieldRef{Name: "model"}, spec.Encodings.Color)
	assert.Equal(t, []types.OrderField{{Field: "sum(tokens_in)", Desc: true}}, spec.Sort)
	assert.Equal(t, types.GrainDay, spec.Grain)
	assert.Equal(t, types.MarkBar, spec.Mark)
	assert.Equal(
		t,
		types.FilterClause{Field: "team", Operator: types.OpEq, Value: "t-1"},
		spec.Filter,
	)

	again, err := json.Marshal(spec)
	require.NoError(t, err)
	reparsed, err := chartspec.Parse(again, chartCubeMap())
	require.NoError(t, err)
	assert.Equal(t, spec, reparsed)
}

// nested wraps a leaf in depth cross operators.
func nested(depth int) string {
	return strings.Repeat(
		`{"op":"cross","args":[`,
		depth,
	) + `{"field":"team"}` + strings.Repeat(
		`]}`,
		depth,
	)
}

// TestChartSpecParse_RejectsWithCodedInvalidInput tests that every class of bad spec is
// rejected with CodeInvalidInput rather than dropped or panicking.
//
// Why this test is important:
//   - The spec arrives from the browser; the allow-list is the only thing between
//     it and the store, so an unknown field or aggregate must never reach a query
//
// What it tests:
//   - an unknown field, a disallowed aggregate, an aggregate on a dimension, a
//     measure without an aggregate, an over-deep shelf (9 levels), a group-by
//     (detail) on an undeclared or non-dimension field, an unknown operator, grain
//     or mark, an inverted time range, a bad filter field, a sort on an unknown
//     key, on a raw (un-aggregated) measure or on a dimension on no shelf, and a
//     nil allow-list are each a CodeInvalidInput error; a shelf of depth 8 is
//     accepted
func TestChartSpecParse_RejectsWithCodedInvalidInput(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"unknown field": `{"cube":"genai_calls","rows":{"field":"cost"}}`,
		"disallowed aggregate": `{"cube":"genai_calls","rows":{"field":"tokens_in","agg":"` +
			`p99"}}`,
		"aggregate on dimension": `{"cube":"genai_calls","rows":{"field":"team","agg":"sum"}}`,
		"measure without agg":    `{"cube":"genai_calls","rows":{"field":"tokens_in"}}`,
		"over-deep nesting":      `{"cube":"genai_calls","rows":` + nested(9) + `}`,
		"undeclared group-by":    `{"cube":"genai_calls","detail":[{"field":"region"}]}`,
		"group-by on a measure":  `{"cube":"genai_calls","detail":[{"field":"tokens_in"}]}`,
		"unknown operator": `{"cube":"genai_calls","rows":{"op":"join","args":[{"field` +
			`":"team"}]}}`,
		"operator without args": `{"cube":"genai_calls","rows":{"op":"cross","args":[]}}`,
		"unknown grain":         `{"cube":"genai_calls","grain":"fortnight"}`,
		"unknown mark":          `{"cube":"genai_calls","mark":"pie"}`,
		"inverted time range": `{"cube":"genai_calls","time_range":{"from":"2026-10-09T00` +
			`:00:00Z","to":"2026-10-01T00:00:00Z"}}`,
		"bad filter field":    `{"cube":"genai_calls","filter":{"$eq":{"secret":"x"}}}`,
		"sort on unknown key": `{"cube":"genai_calls","sort":[{"field":"avg(tokens_in)"}]}`,
		"sort on raw measure": `{"cube":"genai_calls","rows":{"field":"team"},"sort":[{"f` +
			`ield":"tokens_in"}]}`,
		"sort off the shelves": `{"cube":"genai_calls","rows":{"field":"team"},"sort":[{"f` +
			`ield":"model"}]}`,
		"missing cube":   `{"rows":{"field":"team"}}`,
		"malformed JSON": `{"cube":`,
	}
	for name, body := range cases {
		_, err := chartspec.Parse([]byte(body), chartCubeMap())
		require.Error(t, err, name)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err), name)
	}

	_, err := chartspec.Parse(
		[]byte(`{"cube":"genai_calls","rows":`+nested(7)+`}`),
		chartCubeMap(),
	)
	require.NoError(t, err, "depth 8 is within the limit")

	_, err = chartspec.Parse(
		[]byte(`{"cube":"genai_calls","rows":{"field":"team"}}`),
		nil,
	)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err), "nil allow-list")
}

// TestParse_StructuredFilterEqualsGrammarFilter tests that a spec whose filter is built
// as a FilterNode tree and one whose filter arrives as grammar JSON compile to the same
// Plan.
//
// Why this test is important:
//   - A list endpoint and a chart share one filter core; a structured client (the
//     typed filter bar) and a grammar client must not get different queries for the
//     same predicate
//
// What it tests:
//   - the FilterNode-built spec round-trips through the wire form into the same tree
//   - both specs parse and compile to deep-equal Plans, filter included
func TestParse_StructuredFilterEqualsGrammarFilter(t *testing.T) {
	t.Parallel()

	tree := &types.FilterNode{And: []types.FilterNode{
		{Clause: &types.FilterNodeClause{Field: "team", Op: types.OpEq, Value: "t-1"}},
		{Clause: &types.FilterNodeClause{
			Field: "model", Op: types.OpIn, Value: []any{"m-1", "m-2"},
		}},
	}}
	built := types.ChartSpec{
		Cube:       "genai_calls",
		Rows:       leaf("team", types.AggNone),
		Columns:    leaf("tokens_in", types.AggSum),
		FilterTree: tree,
	}
	builtJSON, err := json.Marshal(built)
	require.NoError(t, err)

	const grammarJSON = `{"cube":"genai_calls","rows":{"field":"team"},
	  "columns":{"field":"tokens_in","agg":"sum"},
	  "filter":{"$and":[{"$eq":{"team":"t-1"}},{"$in":{"model":["m-1","m-2"]}}]}}`

	fromTree, err := chartspec.Parse(builtJSON, chartCubeMap())
	require.NoError(t, err)
	fromGrammar, err := chartspec.Parse([]byte(grammarJSON), chartCubeMap())
	require.NoError(t, err)
	assert.Equal(t, tree, fromTree.FilterTree)

	treePlan, err := chartspec.Compile(fromTree, chartCube())
	require.NoError(t, err)
	grammarPlan, err := chartspec.Compile(fromGrammar, chartCube())
	require.NoError(t, err)
	assert.Equal(t, grammarPlan, treePlan)
	assert.NotNil(t, treePlan.Query.Filter)
}
