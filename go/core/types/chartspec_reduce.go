package types

// Stack is how the values of one group are laid end to end; the empty value draws
// them unstacked.
type Stack string

const (
	// StackNone draws each value from zero.
	StackNone Stack = ""
	// StackZero stacks values from zero upward, each starting where the last ended.
	StackZero Stack = "zero"
	// StackNormalize stacks values and scales each group to span one.
	StackNormalize Stack = "normalize"
	// StackDiverging stacks values away from zero in both directions by their
	// polarity: negative below, positive above, neutral split across both.
	StackDiverging Stack = "diverging"
)

// Percent is the share a value is shown as; the empty value shows the value itself.
type Percent string

const (
	// PercentNone shows values as they are.
	PercentNone Percent = ""
	// PercentOfTotal shows each value as its share of the whole.
	PercentOfTotal Percent = "percent_of_total"
)

// Reduce shapes the cells a query returns before they are drawn: it hides small
// cells and turns values into shares.
type Reduce struct {
	// Percent turns values into shares.
	Percent Percent `json:"percent,omitempty"`
	// MinCell hides a cell backed by fewer observations than this; zero hides none.
	MinCell int64 `json:"min_cell,omitempty"`
}
