package chartspec

import (
	"slices"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
)

// Drill returns a copy of spec with the dimension field replaced, wherever it is
// used as a dimension (shelves, detail, encodings, sort keys), by its child in
// the cube's hierarchy. The input spec is not modified. A cube without Fields, a
// field in no hierarchy, at a hierarchy's last level, or whose child the cube does
// not declare is CodeInvalidInput.
// spec must not be nil.
func Drill(
	spec *types.ChartSpec,
	cube listquery.Schema,
	field string,
) (types.ChartSpec, error) {
	child, err := drillChild(&cube, field)
	if err != nil {
		return types.ChartSpec{}, err
	}

	swap := func(ref types.FieldRef) types.FieldRef {
		if ref.Name == field && ref.Agg == types.AggNone {
			ref.Name = child
		}

		return ref
	}
	out := *spec

	out.Rows, out.Columns = drillShelf(spec.Rows, swap), drillShelf(spec.Columns, swap)
	out.Detail = drillDetail(spec.Detail, swap)
	out.Sort = drillSort(spec.Sort, field, child)

	drillEncodings(&out.Encodings, swap)

	return out, nil
}

// drillChild resolves field's child level and checks the cube declares it.
func drillChild(cube *listquery.Schema, field string) (string, error) {
	if cube.Fields == nil {
		return "", invalid("cube " + cube.Name + " has no field allow-list")
	}

	child, ok := childOf(cube.Hierarchies, field)
	if !ok {
		return "", invalid("field " + field + " has no child level to drill into")
	}

	if _, declared := cube.Fields.Lookup(child); !declared {
		return "", invalid("drill child " + child + " is not declared by the cube")
	}

	return child, nil
}

// drillDetail copies the detail list through swap, keeping nil as nil.
func drillDetail(
	detail []types.FieldRef, swap func(types.FieldRef) types.FieldRef,
) []types.FieldRef {
	if detail == nil {
		return nil
	}

	out := make([]types.FieldRef, len(detail))
	for i, ref := range detail {
		out[i] = swap(ref)
	}

	return out
}

// drillSort copies the sort list with field renamed to child, keeping nil as nil.
func drillSort(sorts []types.OrderField, field, child string) []types.OrderField {
	if sorts == nil {
		return nil
	}

	out := slices.Clone(sorts)

	for i := range out {
		if out[i].Field == field {
			out[i].Field = child
		}
	}

	return out
}

// drillEncodings replaces each set channel with a swapped copy.
func drillEncodings(enc *types.Encodings, swap func(types.FieldRef) types.FieldRef) {
	for _, slot := range []**types.FieldRef{&enc.Color, &enc.Size, &enc.Shape, &enc.Label} {
		if *slot != nil {
			swapped := swap(**slot)

			*slot = &swapped
		}
	}
}

// childOf returns the level after field in the first hierarchy containing it.
func childOf(hierarchies [][]string, field string) (string, bool) {
	for _, path := range hierarchies {
		for i, level := range path {
			if level == field && i+1 < len(path) {
				return path[i+1], true
			}
		}
	}

	return "", false
}

// drillShelf deep-copies a shelf expression, applying swap to every leaf.
func drillShelf(
	algebraExpr *types.AlgebraExpr,
	swap func(types.FieldRef) types.FieldRef,
) *types.AlgebraExpr {
	if algebraExpr == nil {
		return nil
	}

	if algebraExpr.Field != nil {
		ref := swap(*algebraExpr.Field)

		return &types.AlgebraExpr{Field: &ref, Op: "", Args: nil}
	}

	args := make([]*types.AlgebraExpr, len(algebraExpr.Args))
	for i, arg := range algebraExpr.Args {
		args[i] = drillShelf(arg, swap)
	}

	return &types.AlgebraExpr{Op: algebraExpr.Op, Args: args, Field: nil}
}
