package chartspec

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Shaped is a plan's cells after the privacy floor, the share and the stack.
type Shaped struct {
	// Cells are the cells in input order, with small ones suppressed and values
	// turned into shares when the plan asks for them.
	Cells []Cell
	// Segments are the stacked extents of Cells, one per cell in the same order.
	Segments []Segment
}

// Shape applies the plan's Reduce and Stack to the cells its Query returned, in
// that order: cells backed by fewer observations than Reduce.MinCell are
// suppressed, values become shares of what remains when Reduce.Percent asks for
// it, and the result is laid out by Stack. counts answers the population the
// floor is checked against and may be nil only when MinCell is zero; a population
// below the floor is CodeFailedPrecondition, a floor with no counts
// CodeInvalidInput. The input cells are not modified.
func (p *Plan) Shape(
	ctx context.Context,
	cells []Cell,
	counts interfaces.CountSource,
) (Shaped, error) {
	cells, err := ApplyMinCell(
		ctx,
		cells,
		MinCellPolicy{Count: counts, Min: p.Reduce.MinCell},
		p.Query.Cube,
		p.Filter,
	)
	if err != nil {
		return Shaped{Cells: nil, Segments: nil}, err
	}

	if p.Reduce.Percent == types.PercentOfTotal {
		cells = PercentOfTotal(cells)
	}

	segments, err := StackCells(cells, p.Stack, p.Polarity)
	if err != nil {
		return Shaped{Cells: nil, Segments: nil}, err
	}

	return Shaped{Cells: cells, Segments: segments}, nil
}
