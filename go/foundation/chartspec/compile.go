package chartspec

import (
	"slices"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
)

// PaneKey identifies one pane: the Key-joined row tuple and column tuple
// ("team,model" / "sum(tokens_in)"); an empty shelf contributes "".
type PaneKey struct {
	// Row is the row tuple's field keys, comma-joined.
	Row string
	// Column is the column tuple's field keys, comma-joined.
	Column string
}

// Plan is a compiled spec: the one AggregateQuery the store runs, the panes in
// row-major order, and the mark of each pane. Query.OrgID is left empty; the
// caller sets the verified tenant.
type Plan struct {
	// Marks is the mark drawn in each pane.
	Marks map[PaneKey]types.Mark
	// Panes are the pane keys, rows outer, columns inner.
	Panes []PaneKey
	// Query is the store query answering every pane.
	Query types.AggregateQuery
}

// Compile turns a parsed spec into its Plan: the pane table is rows × columns of
// the normalized shelves; the query groups by every shelf dimension (and the time
// field) plus Detail and encoded dimensions, computes every shelf and encoded
// measure, and carries the filter, range, grain and sort. Each pane's mark is the
// spec's Mark or the inference O×O→text, O×Q→bar, T×Q→line, Q×Q→point. A cube
// mismatch, a cube without Fields, an undeclared grain, a count_distinct measure
// (which additive partials cannot answer) or a sort key the query neither groups
// by nor computes is CodeInvalidInput. The cube is the listquery.Schema that list
// endpoints use too.
// spec must not be nil.
func Compile(specRef *types.ChartSpec, cube listquery.Schema) (Plan, error) {
	spec := *specRef

	err := checkCube(&spec, &cube)
	if err != nil {
		return Plan{}, err
	}

	rows, cols := Normalize(spec.Rows), Normalize(spec.Columns)

	query, err := aggregateQuery(&spec, rows, cols)
	if err != nil {
		return Plan{}, err
	}

	plan := Plan{Query: query, Marks: make(map[PaneKey]types.Mark, len(rows)*len(cols))}
	for _, r := range rows {
		addRowPanes(&plan, r, cols, spec.Mark, cube.Fields)
	}

	return plan, nil
}

// checkCube rejects a spec the cube cannot answer.
func checkCube(spec *types.ChartSpec, cube *listquery.Schema) error {
	if cube.Fields == nil {
		return invalid("cube " + cube.Name + " has no field allow-list")
	}

	if spec.Cube != cube.Name {
		return invalid("spec cube " + spec.Cube + " does not match " + cube.Name)
	}

	if spec.Grain != "" && len(cube.Grains) > 0 &&
		!slices.Contains(cube.Grains, spec.Grain) {
		return invalid(
			"cube " + cube.Name + " is not rolled up at grain " + string(spec.Grain),
		)
	}

	return nil
}

// addRowPanes adds one pane per column of row r, each with its mark.
func addRowPanes(
	plan *Plan,
	tuple Tuple,
	cols []Tuple,
	mark types.Mark,
	fields *listquery.Map,
) {
	for _, c := range cols {
		key := PaneKey{Row: tupleKey(tuple), Column: tupleKey(c)}

		plan.Panes = append(plan.Panes, key)
		plan.Marks[key] = markFor(mark, kindOf(tuple, fields), kindOf(c, fields))
	}
}

// aggregateQuery derives the group-by and measure lists of the spec's one query
// and checks that every sort key is one of them.
func aggregateQuery(
	specRef *types.ChartSpec,
	rows, cols []Tuple,
) (types.AggregateQuery, error) {
	spec := *specRef

	queryBuilderValue := queryBuilder{
		query: types.AggregateQuery{
			Cube:      spec.Cube,
			Filter:    spec.Filter,
			TimeRange: spec.TimeRange,
			Grain:     spec.Grain,
			Sort:      spec.Sort,
		},
		seenMeasure: map[string]bool{},
	}

	for _, ref := range queryRefs(&spec, rows, cols) {
		err := queryBuilderValue.add(ref)
		if err != nil {
			return types.AggregateQuery{}, err
		}
	}

	for _, o := range spec.Sort {
		if !slices.Contains(queryBuilderValue.query.GroupBy, o.Field) &&
			!queryBuilderValue.seenMeasure[o.Field] {
			return types.AggregateQuery{}, invalid(
				"sort key " + o.Field + " is neither grouped by nor computed",
			)
		}
	}

	return queryBuilderValue.query, nil
}

// queryBuilder accumulates the group-by and measure lists of a query.
type queryBuilder struct {
	// seenMeasure holds the measure keys already added.
	seenMeasure map[string]bool

	// query is the query built so far.
	query types.AggregateQuery
}

// add files ref as a group-by or a measure, once each; count_distinct is rejected.
func (b *queryBuilder) add(ref types.FieldRef) error {
	if ref.Agg == types.AggNone {
		if !slices.Contains(b.query.GroupBy, ref.Name) {
			b.query.GroupBy = append(b.query.GroupBy, ref.Name)
		}

		return nil
	}

	if ref.Agg == types.AggCountDistinct {
		return invalid("count_distinct is not answerable from additive partials")
	}

	if !b.seenMeasure[ref.Key()] {
		b.seenMeasure[ref.Key()] = true
		b.query.Measures = append(b.query.Measures, types.MeasureRef(ref))
	}

	return nil
}

// queryRefs lists every field the query touches: shelves, detail, then encodings.
func queryRefs(spec *types.ChartSpec, rows, cols []Tuple) []types.FieldRef {
	var refs []types.FieldRef

	for _, t := range slices.Concat(rows, cols) {
		refs = append(refs, t.Fields...)
	}

	return append(append(refs, spec.Detail...), spec.Encodings.Fields()...)
}

// tupleKey comma-joins a tuple's field keys.
func tupleKey(t Tuple) string {
	keys := make([]string, 0, len(t.Fields))
	for _, f := range t.Fields {
		keys = append(keys, f.Key())
	}

	return strings.Join(keys, ",")
}
