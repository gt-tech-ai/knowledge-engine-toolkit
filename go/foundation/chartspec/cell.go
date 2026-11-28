package chartspec

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Cell is one value a chart draws, with the number of observations behind it.
type Cell struct {
	// Key names the cell: the dimension values that locate it, in shelf order.
	Key []string
	// Value is what the cell shows: an aggregate, or a share after PercentOfTotal.
	Value float64
	// Count is how many observations the value summarises.
	Count int64
	// Suppressed marks a cell hidden by the privacy floor; its Value is zero.
	Suppressed bool
}

// MinCellPolicy is the privacy floor a chart enforces before it shows a cell.
type MinCellPolicy struct {
	// Count reads the population the chart covers; it is required when Min is set.
	Count interfaces.CountSource
	// Min is the fewest observations a cell, and the whole population, may rest on;
	// zero turns the floor off.
	Min int64
}

// ApplyMinCell hides every cell backed by fewer than policy.Min observations,
// setting it Suppressed with value zero, and returns the cells in a new slice. A
// population (cube under filter) smaller than the floor is refused whole with
// CodeFailedPrecondition, because every cell of it would be too small to show. A
// floor with no count source is CodeInvalidInput. A floor of zero returns the
// cells unchanged without reading the population.
func ApplyMinCell(
	ctx context.Context,
	cells []Cell,
	policy MinCellPolicy,
	cube string,
	filter *types.FilterNode,
) ([]Cell, error) {
	out := make([]Cell, len(cells))
	copy(out, cells)

	if policy.Min <= 0 {
		return out, nil
	}

	if policy.Count == nil {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"chartspec: min_cell needs a count source",
		)
	}

	population, err := policy.Count.Count(ctx, cube, filter)
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.ContextCode(err, apperr.CodeInternal),
			"chartspec: count population",
		)
	}

	if population < policy.Min {
		return nil, apperr.New(
			apperr.CodeFailedPrecondition,
			"chartspec: population is below the minimum cell size",
		)
	}

	for i := range out {
		if out[i].Count < policy.Min {
			out[i].Suppressed = true
			out[i].Value = 0
		}
	}

	return out, nil
}

// PercentOfTotal replaces each visible cell's value with its share of the visible
// total and returns the cells in a new slice. Suppressed cells stay at zero and are
// left out of the total, so a hidden cell cannot be backed out of the shares. A
// total of zero leaves every value at zero.
func PercentOfTotal(cells []Cell) []Cell {
	out := make([]Cell, len(cells))
	copy(out, cells)

	var total float64

	for _, c := range out {
		if !c.Suppressed {
			total += c.Value
		}
	}

	for i := range out {
		switch {
		case out[i].Suppressed, total == 0:
			out[i].Value = 0
		default:
			out[i].Value /= total
		}
	}

	return out
}
