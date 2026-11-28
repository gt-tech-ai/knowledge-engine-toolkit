package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/chartspec"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
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
	return &types.AlgebraExpr{
		Field: &types.FieldRef{Name: name, Agg: agg},
		Op:    "",
		Args:  nil,
	}
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
		{
			name: "Q alone",
			rows: leaf(textTokensIn, types.AggSum),
			want: types.MarkBar,
			cols: nil,
		},
	}
	for _, testCase := range cases {
		plan, err := chartspec.Compile(
			fixtures.BuildRef(func(row *types.ChartSpec) {
				row.Cube = textGenaiCalls
				row.Rows = testCase.rows
				row.Columns = testCase.cols
			}),
			chartCube(),
		)
		require.NoError(t, err, testCase.name)
		require.Len(t, plan.Panes, 1, testCase.name)
		assert.Equal(t, testCase.want, plan.Marks[plan.Panes[0]], testCase.name)
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

	plan, err := chartspec.Compile(fixtures.BuildRef(func(row *types.ChartSpec) {
		row.Cube = textGenaiCalls
		row.Rows = leaf(textTeam, "")
		row.Columns = leaf(textTokensIn, types.AggSum)
		row.Mark = types.MarkLine
	}), chartCube())
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

	plan, err := chartspec.Compile(&spec, chartCube())
	require.NoError(t, err)

	assert.Equal(t, fixtures.Build(func(row *types.AggregateQuery) {
		row.Cube = textGenaiCalls
		row.GroupBy = []string{textTeam, textModel, textWorkspace}
		row.Measures = []types.MeasureRef{{Name: textTokensIn, Agg: types.AggSum}}
		row.Filter = spec.Filter
		row.TimeRange = spec.TimeRange
		row.Grain = types.GrainDay
		row.Sort = spec.Sort
	}), plan.Query)
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
	cube.Fields.Add(new(listquery.String(textUserID)).AsMeasure(types.AggCountDistinct))

	for name, spec := range map[string]types.ChartSpec{
		"cube mismatch": fixtures.Build(func(row *types.ChartSpec) {
			row.Cube = textOther
			row.Rows = leaf(textTeam, "")
		}),
		textGrain: fixtures.Build(func(row *types.ChartSpec) {
			row.Cube = textGenaiCalls
			row.Rows = leaf(textTeam, "")
			row.Grain = types.GrainMonth
		}),
		"count_distinct": fixtures.Build(func(row *types.ChartSpec) {
			row.Cube = textGenaiCalls
			row.Rows = leaf(textUserID,
				types.AggCountDistinct)
		}),
		"sort off the query": fixtures.Build(func(row *types.ChartSpec) {
			row.Cube = textGenaiCalls
			row.Rows = leaf(textTeam, "")
			row.Sort = []types.OrderField{fixtures.Build(func(row *types.OrderField) {
				row.Field = textModel
			})}
		}),
	} {
		_, err := chartspec.Compile(&spec, cube)
		require.Error(t, err, name)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err), name)
	}

	_, err := chartspec.Compile(
		fixtures.BuildRef(func(row *types.ChartSpec) {
			row.Cube = textGenaiCalls
			row.Rows = leaf(textTeam, "")
		}),
		fixtures.Build(func(row *listquery.Schema) {
			row.Name = textGenaiCalls
		}),
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

	spec := fixtures.Build(func(row *types.ChartSpec) {
		row.Cube = textGenaiCalls
		row.Rows = &types.AlgebraExpr{
			Op:    types.AlgebraCross,
			Args:  []*types.AlgebraExpr{leaf(textTeam, ""), leaf(textModel, "")},
			Field: nil,
		}
		row.Detail = []types.FieldRef{{Name: textTeam, Agg: ""}}
		row.Sort = []types.OrderField{
			fixtures.Build(func(row *types.OrderField) {
				row.Field = textTeam
				row.Desc = true
			}),
			fixtures.Build(func(row *types.OrderField) {
				row.Field = textSumInputCount
			}),
		}
	})

	drilled, err := chartspec.Drill(&spec, chartCube(), textTeam)
	require.NoError(t, err)
	assert.Equal(
		t,
		[]types.OrderField{
			fixtures.Build(func(row *types.OrderField) {
				row.Field = textWorkspace
				row.Desc = true
			}),
			fixtures.Build(func(row *types.OrderField) {
				row.Field = textSumInputCount
			}),
		},
		drilled.Sort,
	)

	testDrillTeamToWorkspaceChecksSpec(t, &spec, &drilled)
}

// testDrillTeamToWorkspaceChecksSpec continues TestDrill_TeamToWorkspace.
func testDrillTeamToWorkspaceChecksSpec(t *testing.T,
	spec *types.ChartSpec,
	drilled *types.ChartSpec,
) {
	t.Helper()

	assert.Equal(t, textTeam, spec.Sort[0].Field, "input sort unchanged")

	_, err := chartspec.Drill(
		spec,
		fixtures.Build(func(row *listquery.Schema) {
			row.Hierarchies = chartCube().Hierarchies
		}),
		textTeam,
	)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err), "nil allow-list")

	assert.Equal(
		t,
		&types.FieldRef{Name: textWorkspace, Agg: ""},
		drilled.Rows.Args[0].Field,
	)
	assert.Equal(t, &types.FieldRef{Name: textModel, Agg: ""}, drilled.Rows.Args[1].Field)
	assert.Equal(t, []types.FieldRef{{Name: textWorkspace, Agg: ""}}, drilled.Detail)
	assert.Equal(
		t,
		&types.FieldRef{Name: textTeam, Agg: ""},
		spec.Rows.Args[0].Field,
		"input spec unchanged",
	)
	assert.Equal(
		t,
		[]types.FieldRef{{Name: textTeam, Agg: ""}},
		spec.Detail,
		"input detail unchanged",
	)

	for _, field := range []string{textWorkspace, textModel} {
		_, err := chartspec.Drill(drilled, chartCube(), field)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err), field)
	}
}

const (
	// shapeSentiment is the dimension the stacking tests give a polarity.
	shapeSentiment = "sentiment"
	// shapeNegative, shapeNeutral and shapePositive are its values.
	shapeNegative = "negative"
	shapeNeutral  = "neutral"
	shapePositive = "positive"
)

// shapeSigns is the polarity of the sentiment values: -1, 0 and +1.
func shapeSigns() map[string]int8 {
	return map[string]int8{shapeNegative: -1, shapeNeutral: 0, shapePositive: 1}
}

// shapeCube is the test cube with a sentiment dimension declaring polarity signs.
func shapeCube(signs map[string]int8) listquery.Schema {
	cube := chartCube()
	cube.Fields.Add(new(listquery.String(shapeSentiment)).WithPolarity(signs))

	return cube
}

// shapeSpec is a team × tokens_in bar coloured by the sentiment dimension and stacked
// by mode.
func shapeSpec(mode types.Stack) types.ChartSpec {
	return fixtures.Build(func(row *types.ChartSpec) {
		row.Cube = textGenaiCalls
		row.Rows = leaf(textTeam, "")
		row.Columns = leaf(textTokensIn, types.AggSum)
		row.Mark = types.MarkBar
		row.Stack = mode
		row.Encodings.Color = &types.FieldRef{Name: shapeSentiment, Agg: ""}
	})
}

// TestCompile_CellMarkNeedsDimensionsAndAMeasureColour tests the cell mark's shape.
//
// Why this test is important:
//   - A cell grid is dimension × dimension coloured by a measure; a measure on a shelf
//     would draw one cell per value instead of one per pair, with no error to say so
//
// What it tests:
//   - team × model with a summed colour compiles to the cell mark in its one pane
//   - a measure on a shelf, or a colour that is not a measure, is CodeInvalidInput
func TestCompile_CellMarkNeedsDimensionsAndAMeasureColour(t *testing.T) {
	t.Parallel()

	cellSpec := func(edit func(row *types.ChartSpec)) types.ChartSpec {
		return fixtures.Build(func(row *types.ChartSpec) {
			row.Cube = textGenaiCalls
			row.Rows = leaf(textTeam, "")
			row.Columns = leaf(textModel, "")
			row.Mark = types.MarkCell
			row.Encodings.Color = &types.FieldRef{Name: textTokensIn, Agg: types.AggSum}
			edit(row)
		})
	}

	good := cellSpec(func(*types.ChartSpec) {})
	plan, err := chartspec.Compile(&good, chartCube())
	require.NoError(t, err)
	require.Len(t, plan.Panes, 1)
	assert.Equal(t, types.MarkCell, plan.Marks[plan.Panes[0]])

	for name, spec := range map[string]types.ChartSpec{
		"measure on a shelf": cellSpec(func(row *types.ChartSpec) {
			row.Columns = leaf(textTokensIn, types.AggSum)
		}),
		"colour is a dimension": cellSpec(func(row *types.ChartSpec) {
			row.Encodings.Color = &types.FieldRef{Name: textTeam, Agg: ""}
		}),
		"no colour": cellSpec(func(row *types.ChartSpec) {
			row.Encodings.Color = nil
		}),
	} {
		_, err = chartspec.Compile(&spec, chartCube())
		require.Error(t, err, name)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err), name)
	}
}

// TestCompile_DivergingStackNeedsPolarity tests the polarity check of a diverging stack.
//
// Why this test is important:
//   - Without a polarity a diverging stack cannot tell negative from positive and
//     would stack everything on one side; refusing at compile names the field to fix
//
// What it tests:
//   - a diverging stack coloured by a field with no polarity is CodeInvalidInput and
//     the message names the field
//   - a polarity value outside -1, 0, +1 is CodeInvalidInput
//   - a field with a full polarity compiles, and so do zero and normalize stacks on a
//     field without one
func TestCompile_DivergingStackNeedsPolarity(t *testing.T) {
	t.Parallel()

	spec := shapeSpec(types.StackDiverging)

	_, err := chartspec.Compile(&spec, chartCube())
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	_, err = chartspec.Compile(&spec, shapeCube(nil))
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.Contains(t, err.Error(), shapeSentiment)

	_, err = chartspec.Compile(&spec, shapeCube(map[string]int8{shapeNegative: -2}))
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	_, err = chartspec.Compile(&spec, shapeCube(shapeSigns()))
	require.NoError(t, err)

	for _, mode := range []types.Stack{types.StackZero, types.StackNormalize} {
		plain := shapeSpec(mode)

		_, err = chartspec.Compile(&plain, shapeCube(nil))
		require.NoError(t, err, string(mode))
	}
}
