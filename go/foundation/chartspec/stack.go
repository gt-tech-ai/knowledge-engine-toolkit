package chartspec

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// neutralSides is how many sides of zero a neutral band is split across.
const neutralSides = 2

// Segment is the span one cell fills on the value axis. A segment drawn below zero
// has End less than Start.
type Segment struct {
	// Key names the cell the segment draws.
	Key []string
	// Start is where the segment begins on the value axis.
	Start float64
	// End is where the segment stops on the value axis.
	End float64
}

// StackCells lays the cells of one group end to end under mode and returns one
// segment per cell, in cell order.
//
//   - StackNone draws each cell from zero to its value.
//   - StackZero starts each cell where the last ended, from zero.
//   - StackNormalize does the same after scaling the group to span one; a group
//     whose values total zero stays at zero.
//   - StackDiverging puts cells whose polarity is negative below zero, positive
//     above it, and neutral across it, each side growing away from zero in cell
//     order. polarity receives the last element of a cell's Key.
//
// A stacking mode rejects a negative value; diverging with no polarity, or a value
// polarity does not know, and an unknown mode are CodeInvalidInput.
func StackCells(
	cells []Cell,
	mode types.Stack,
	polarity func(string) (int8, bool),
) ([]Segment, error) {
	if mode != types.StackNone {
		err := checkStackable(cells)
		if err != nil {
			return nil, err
		}
	}

	switch mode {
	case types.StackNone:
		return stackUnstacked(cells), nil
	case types.StackZero:
		return stackEndToEnd(cells, 1), nil
	case types.StackNormalize:
		return stackEndToEnd(cells, normalizeScale(cells)), nil
	case types.StackDiverging:
		return stackDiverging(cells, polarity)
	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"chartspec: unknown stack mode "+string(mode),
		)
	}
}

// checkStackable refuses a negative value, which a stack cannot lay end to end.
func checkStackable(cells []Cell) error {
	for _, cell := range cells {
		if cell.Value < 0 {
			return apperr.New(
				apperr.CodeInvalidInput,
				"chartspec: a stacked value must not be negative",
			)
		}
	}

	return nil
}

// normalizeScale returns the factor that makes the cells span one: the inverse of
// their total, or zero for a group that totals zero.
func normalizeScale(cells []Cell) float64 {
	var total float64

	for _, cell := range cells {
		total += cell.Value
	}

	if total == 0 {
		return 0
	}

	return 1 / total
}

// stackUnstacked draws each cell from zero to its value.
func stackUnstacked(cells []Cell) []Segment {
	out := make([]Segment, len(cells))

	for at, cell := range cells {
		out[at] = Segment{Key: cell.Key, Start: 0, End: cell.Value}
	}

	return out
}

// stackEndToEnd places each cell's scaled value where the previous one ended,
// starting from zero.
func stackEndToEnd(cells []Cell, scale float64) []Segment {
	out := make([]Segment, len(cells))

	var cursor float64

	for at, cell := range cells {
		out[at] = Segment{Key: cell.Key, Start: cursor, End: cursor + cell.Value*scale}
		cursor = out[at].End
	}

	return out
}

// divergingCursors tracks where the next cell of each polarity starts.
type divergingCursors struct {
	// neutral is where the next neutral cell starts.
	neutral float64
	// below is where the next negative cell starts, growing downward.
	below float64
	// above is where the next positive cell starts, growing upward.
	above float64
}

// place returns the segment for cell c on the side its sign picks, advancing that
// side's cursor.
func (d *divergingCursors) place(cell Cell, sign int8) Segment {
	switch {
	case sign < 0:
		start := d.below

		d.below -= cell.Value

		return Segment{Key: cell.Key, Start: start, End: d.below}
	case sign > 0:
		start := d.above

		d.above += cell.Value

		return Segment{Key: cell.Key, Start: start, End: d.above}
	default:
		start := d.neutral

		d.neutral += cell.Value

		return Segment{Key: cell.Key, Start: start, End: d.neutral}
	}
}

// stackDiverging centres the neutral cells on zero and grows negative cells down
// and positive cells up from the neutral band's edges.
func stackDiverging(
	cells []Cell,
	polarity func(string) (int8, bool),
) ([]Segment, error) {
	if polarity == nil {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"chartspec: a diverging stack needs a polarity",
		)
	}

	signs := make([]int8, len(cells))

	var neutral float64

	for pos, cell := range cells {
		sign, err := cellPolarity(cell, polarity)
		if err != nil {
			return nil, err
		}

		signs[pos] = sign
		if sign == 0 {
			neutral += cell.Value
		}
	}

	edge := neutral / neutralSides
	cursors := divergingCursors{neutral: -edge, below: -edge, above: edge}
	out := make([]Segment, len(cells))

	for pos, cell := range cells {
		out[pos] = cursors.place(cell, signs[pos])
	}

	return out, nil
}

// cellPolarity looks up the polarity of the cell's colour value, the last element
// of its Key.
func cellPolarity(cell Cell, polarity func(string) (int8, bool)) (int8, error) {
	if len(cell.Key) == 0 {
		return 0, apperr.New(
			apperr.CodeInvalidInput,
			"chartspec: a diverging cell needs a colour value in its key",
		)
	}

	colour := cell.Key[len(cell.Key)-1]

	sign, found := polarity(colour)
	if !found {
		return 0, apperr.New(
			apperr.CodeInvalidInput,
			"chartspec: no polarity for value "+colour,
		)
	}

	return sign, nil
}
