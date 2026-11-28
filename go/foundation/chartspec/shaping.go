package chartspec

import (
	"slices"
	"strconv"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
)

// checkShaping rejects a spec whose cell mark or stack the cube cannot honour: a
// cell mark needs dimensions on both shelves and a measure as colour, and a
// diverging stack needs a colour dimension that declares a polarity.
func checkShaping(
	spec *types.ChartSpec,
	cube *listquery.Schema,
	rows, cols []Tuple,
) error {
	if spec.Mark == types.MarkCell {
		err := checkCell(spec, cube.Fields, slices.Concat(rows, cols))
		if err != nil {
			return err
		}
	}

	if spec.Stack == types.StackDiverging {
		return checkPolarity(spec.Encodings.Color, cube.Fields)
	}

	return nil
}

// checkCell requires every shelf tuple to be a dimension and the colour a measure.
func checkCell(spec *types.ChartSpec, fields *listquery.Map, tuples []Tuple) error {
	for _, t := range tuples {
		if kindOf(t, fields) != kindOrdinal {
			return invalid(
				"a cell mark takes dimensions on both shelves, not " + tupleKey(t),
			)
		}
	}

	if color := spec.Encodings.Color; color == nil || color.Agg == types.AggNone {
		return invalid("a cell mark is coloured by a measure")
	}

	return nil
}

// checkPolarity requires the colour to be a dimension whose polarity values are all
// -1, 0 or +1.
func checkPolarity(color *types.FieldRef, fields *listquery.Map) error {
	if color == nil || color.Agg != types.AggNone {
		return invalid("a diverging stack is coloured by a dimension with a polarity")
	}

	field, ok := fields.Lookup(color.Name)
	if !ok || len(field.Signs) == 0 {
		return invalid("a diverging stack needs a polarity on field " + color.Name)
	}

	return checkSigns(color.Name, field.Signs)
}

// checkSigns requires every polarity of the named field to be -1, 0 or +1.
func checkSigns(name string, signs map[string]int8) error {
	for value, sign := range signs {
		if sign < -1 || sign > 1 {
			return invalid("polarity of " + name + "=" + value + " is " +
				strconv.Itoa(int(sign)) + ", want -1, 0 or 1")
		}
	}

	return nil
}
