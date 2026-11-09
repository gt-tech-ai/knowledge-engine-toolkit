package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/chartspec"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
)

// chartCube is the genai_calls test cube: its allow-list, the team → workspace
// hierarchy and the grains it is rolled up at.
func chartCube() listquery.Schema {
	return listquery.Schema{
		Name:        textGenaiCalls,
		Fields:      chartCubeMap(),
		Hierarchies: [][]string{{textTeam, textWorkspace}},
		Grains:      []types.Grain{types.GrainHour, types.GrainDay},
	}
}

// leaf builds a shelf leaf.
func leaf(name string, agg types.Aggregate) *types.AlgebraExpr {
	return &types.AlgebraExpr{Field: &types.FieldRef{Name: name, Agg: agg}}
}

// TestCompile_InfersMarkPerPaneKind tests the mark each pane gets from the kinds
// of its row and column fields.
//
// Why this test is important:
//   - The console draws whatever mark the compiler picks; the wrong inference
//     renders a time series as bars or a table as a scatter
//
// What it tests:
//   - O×O → text, O×Q → bar, Q×O → bar, T×Q → line, Q×Q → point, Q alone → bar
func TestCompile_InfersMarkPerPaneKind(t *testing.T) {
	t.Parallel()

	cases := []struct {
		rows, cols *types.AlgebraExpr
		name       string
		want       types.Mark
	}{
		{
			name: "O×O",
			rows: leaf(textTeam, ""),
			cols: leaf(textModel, ""),
			want: types.MarkText,
		},
		{
			name: "O×Q",
			rows: leaf(textTeam, ""),
			cols: leaf(textTokensIn, types.AggSum),
			want: types.MarkBar,
		},
		{
			name: "Q×O",
			rows: leaf(textTokensIn, types.AggSum),
			cols: leaf(textTeam, ""),
			want: types.MarkBar,
		},
		{
			name: "T×Q",
			rows: leaf("ts", ""),
			cols: leaf("duration_s", types.AggP95),
			want: types.MarkLine,
		},
		{
			name: "Q×Q",
			rows: leaf(textTokensIn, types.AggAvg),
			cols: leaf("duration_s", types.AggAvg),
			want: types.MarkPoint,
		},
		{name: "Q alone", rows: leaf(textTokensIn, types.AggSum), want: types.MarkBar},
	}
	for _, tc := range cases {
		plan, err := chartspec.Compile(
			types.ChartSpec{Cube: textGenaiCalls, Rows: tc.rows, Columns: tc.cols},
			chartCube(),
		)
		require.NoError(t, err, tc.name)
		require.Len(t, plan.Panes, 1, tc.name)
		assert.Equal(t, tc.want, plan.Marks[plan.Panes[0]], tc.name)
	}
}

// TestCompile_ExplicitMarkOverridesInference tests that a spec's Mark wins over
// the inferred mark in every pane.
//
// Why this test is important:
//   - An analyst who asks for a line must get a line even where a bar is inferred
//
// What it tests:
//   - team × sum(tokens_in) with Mark=line yields line, not bar
func TestCompile_ExplicitMarkOverridesInference(t *testing.T) {
	t.Parallel()

	plan, err := chartspec.Compile(types.ChartSpec{
		Cube:    textGenaiCalls,
		Rows:    leaf(textTeam, ""),
		Columns: leaf(textTokensIn, types.AggSum),
		Mark:    types.MarkLine,
	}, chartCube())
	require.NoError(t, err)

	assert.Equal(
		t,
		map[chartspec.PaneKey]types.Mark{
			{Row: textTeam, Column: textSumInputCount}: types.MarkLine,
		},
		plan.Marks,
	)
}

// TestCompile_DerivesOneAggregateQuery tests the single store query a multi-pane
// spec compiles to.
//
// Why this test is important:
//   - Every pane is answered from one query; a missed group-by field collapses
//     rows, an extra one splits them
//
// What it tests:
//   - (team + model) × sum(tokens_in), detail workspace, color model, filter, sort,
//     range, grain → group-by [team, model, workspace] (shelf order, deduplicated),
//     measures [sum(tokens_in)], the filter/range/grain/sort carried over
//   - panes team|sum(tokens_in) and model|sum(tokens_in), both bars
func TestCompile_DerivesOneAggregateQuery(t *testing.T) {
	t.Parallel()

	spec, err := chartspec.Parse([]byte(`{
	  "cube": "genai_calls",
	  "rows": {"op": "concat", "args": [{"field": "team"}, {"field": "model"}]},
	  "columns": {"field": "tokens_in", "agg": "sum"},
	  "detail": [{"field": "workspace"}],
	  "encodings": {"color": {"field": "model"}},
	  "filter": {"$eq": {"team": "t-1"}},
	  "sort": [{"field": "sum(tokens_in)", "desc": true}],
	  "time_range": {"from": "2026-10-01T00:00:00Z", "to": "2026-10-09T00:00:00Z"},
	  "grain": "day"
	}`), chartCubeMap())
	require.NoError(t, err)

	plan, err := chartspec.Compile(spec, chartCube())
	require.NoError(t, err)

	assert.Equal(t, types.AggregateQuery{
		Cube:      textGenaiCalls,
		GroupBy:   []string{textTeam, textModel, textWorkspace},
		Measures:  []types.MeasureRef{{Name: textTokensIn, Agg: types.AggSum}},
		Filter:    spec.Filter,
		TimeRange: spec.TimeRange,
		Grain:     types.GrainDay,
		Sort:      spec.Sort,
	}, plan.Query)
	assert.Equal(
		t,
		[]chartspec.PaneKey{
			{Row: textTeam, Column: textSumInputCount},
			{Row: textModel, Column: textSumInputCount},
		},
		plan.Panes,
	)
	assert.Equal(
		t,
		types.MarkBar,
		plan.Marks[chartspec.PaneKey{Row: textModel, Column: textSumInputCount}],
	)
}

// TestCompile_RejectsWithCodedInvalidInput tests the compile-time rejections.
//
// Why this test is important:
//   - A spec for another cube, a grain the cube is not rolled up at, or an
//     aggregate the partials cannot answer must fail loudly, not return wrong data
//
// What it tests:
//   - a cube mismatch, an undeclared grain, count_distinct, a sort on a key the
//     query neither groups by nor computes, and a cube without an allow-list
//     are CodeInvalidInput
func TestCompile_RejectsWithCodedInvalidInput(t *testing.T) {
	t.Parallel()

	cube := chartCube()
	cube.Fields.Add(listquery.String(textUserID).AsMeasure(types.AggCountDistinct))

	for name, spec := range map[string]types.ChartSpec{
		"cube mismatch": {Cube: textOther, Rows: leaf(textTeam, "")},
		textGrain: {
			Cube: textGenaiCalls, Rows: leaf(textTeam, ""), Grain: types.GrainMonth,
		},
		"count_distinct": {Cube: textGenaiCalls, Rows: leaf(textUserID,
			types.AggCountDistinct)},
		"sort off the query": {
			Cube: textGenaiCalls,
			Rows: leaf(textTeam, ""),
			Sort: []types.OrderField{{Field: textModel}},
		},
	} {
		_, err := chartspec.Compile(spec, cube)
		require.Error(t, err, name)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err), name)
	}

	_, err := chartspec.Compile(
		types.ChartSpec{Cube: textGenaiCalls, Rows: leaf(textTeam, "")},
		listquery.Schema{Name: textGenaiCalls},
	)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err), "nil allow-list")
}

// TestDrill_TeamToWorkspace tests drilling a dimension down one hierarchy level.
//
// Why this test is important:
//   - Drill-down is a new spec, not a new RPC; it must swap exactly the drilled
//     field everywhere and leave the caller's spec untouched
//
// What it tests:
//   - drilling "team" in team × model (detail team, sorted by team desc then
//     sum(tokens_in)) yields workspace × model (detail workspace, sorted by workspace
//     desc then sum(tokens_in)); the input spec is unchanged
//   - drilling with a cube that has no allow-list is CodeInvalidInput
//   - drilling the hierarchy's last level, or a field in no hierarchy, is
//     CodeInvalidInput
func TestDrill_TeamToWorkspace(t *testing.T) {
	t.Parallel()

	spec := types.ChartSpec{
		Cube: textGenaiCalls,
		Rows: &types.AlgebraExpr{
			Op:   types.AlgebraCross,
			Args: []*types.AlgebraExpr{leaf(textTeam, ""), leaf(textModel, "")},
		},
		Detail: []types.FieldRef{{Name: textTeam}},
		Sort: []types.OrderField{
			{Field: textTeam, Desc: true},
			{Field: textSumInputCount},
		},
	}

	drilled, err := chartspec.Drill(spec, chartCube(), textTeam)
	require.NoError(t, err)
	assert.Equal(
		t,
		[]types.OrderField{
			{Field: textWorkspace, Desc: true},
			{Field: textSumInputCount},
		},
		drilled.Sort,
	)
	assert.Equal(t, textTeam, spec.Sort[0].Field, "input sort unchanged")

	_, err = chartspec.Drill(
		spec,
		listquery.Schema{Hierarchies: chartCube().Hierarchies},
		textTeam,
	)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err), "nil allow-list")

	assert.Equal(t, &types.FieldRef{Name: textWorkspace}, drilled.Rows.Args[0].Field)
	assert.Equal(t, &types.FieldRef{Name: textModel}, drilled.Rows.Args[1].Field)
	assert.Equal(t, []types.FieldRef{{Name: textWorkspace}}, drilled.Detail)
	assert.Equal(
		t,
		&types.FieldRef{Name: textTeam},
		spec.Rows.Args[0].Field,
		"input spec unchanged",
	)
	assert.Equal(
		t,
		[]types.FieldRef{{Name: textTeam}},
		spec.Detail,
		"input detail unchanged",
	)

	for _, field := range []string{textWorkspace, textModel} {
		_, err := chartspec.Drill(drilled, chartCube(), field)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err), field)
	}
}
