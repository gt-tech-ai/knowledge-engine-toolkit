package chartspec

import (
	"slices"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
)

// CubeSchema is what the compiler knows about one cube: its field allow-list
// (roles and permitted aggregates), its drill hierarchies and the grains it is
// rolled up at. It is configuration data (the consumer's cube catalog).
type CubeSchema struct {
	// Fields is the cube's allow-list (dimensions, measures, the time field).
	Fields *listquery.Map
	// Name is the cube name a spec must address.
	Name string
	// Hierarchies are drill paths, each ordered parent → child (e.g. team, workspace, user).
	Hierarchies [][]string
	// Grains are the grains the cube is rolled up at; empty accepts every grain.
	Grains []types.Grain
}

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
// by nor computes is CodeInvalidInput.
func Compile(spec types.ChartSpec, cube CubeSchema) (Plan, error) {
	if cube.Fields == nil {
		return Plan{}, invalid("cube " + cube.Name + " has no field allow-list")
	}
	if spec.Cube != cube.Name {
		return Plan{}, invalid("spec cube " + spec.Cube + " does not match " + cube.Name)
	}
	if spec.Grain != "" && len(cube.Grains) > 0 &&
		!slices.Contains(cube.Grains, spec.Grain) {
		return Plan{}, invalid(
			"cube " + cube.Name + " is not rolled up at grain " + string(spec.Grain),
		)
	}
	rows, cols := Normalize(spec.Rows), Normalize(spec.Columns)
	query, err := aggregateQuery(spec, rows, cols)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Query: query, Marks: make(map[PaneKey]types.Mark, len(rows)*len(cols))}
	for _, r := range rows {
		for _, c := range cols {
			key := PaneKey{Row: tupleKey(r), Column: tupleKey(c)}
			plan.Panes = append(plan.Panes, key)
			plan.Marks[key] = markFor(
				spec.Mark,
				kindOf(r, cube.Fields),
				kindOf(c, cube.Fields),
			)
		}
	}
	return plan, nil
}

// aggregateQuery derives the group-by and measure lists of the spec's one query
// and checks that every sort key is one of them.
func aggregateQuery(
	spec types.ChartSpec,
	rows, cols []Tuple,
) (types.AggregateQuery, error) {
	q := types.AggregateQuery{
		Cube:      spec.Cube,
		Filter:    spec.Filter,
		TimeRange: spec.TimeRange,
		Grain:     spec.Grain,
		Sort:      spec.Sort,
	}
	seenMeasure := map[string]bool{}
	add := func(ref types.FieldRef) error {
		if ref.Agg == types.AggNone {
			if !slices.Contains(q.GroupBy, ref.Name) {
				q.GroupBy = append(q.GroupBy, ref.Name)
			}
			return nil
		}
		if ref.Agg == types.AggCountDistinct {
			return invalid("count_distinct is not answerable from additive partials")
		}
		if !seenMeasure[ref.Key()] {
			seenMeasure[ref.Key()] = true
			q.Measures = append(q.Measures, types.MeasureRef(ref))
		}
		return nil
	}
	var refs []types.FieldRef
	for _, tuples := range [][]Tuple{rows, cols} {
		for _, t := range tuples {
			refs = append(refs, t.Fields...)
		}
	}
	refs = append(append(refs, spec.Detail...), spec.Encodings.Fields()...)
	for _, ref := range refs {
		if err := add(ref); err != nil {
			return types.AggregateQuery{}, err
		}
	}
	for _, o := range spec.Sort {
		if !slices.Contains(q.GroupBy, o.Field) && !seenMeasure[o.Field] {
			return types.AggregateQuery{}, invalid(
				"sort key " + o.Field + " is neither grouped by nor computed",
			)
		}
	}
	return q, nil
}

// tupleKey comma-joins a tuple's field keys.
func tupleKey(t Tuple) string {
	keys := make([]string, 0, len(t.Fields))
	for _, f := range t.Fields {
		keys = append(keys, f.Key())
	}
	return strings.Join(keys, ",")
}
