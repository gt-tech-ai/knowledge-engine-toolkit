package types

import (
	"encoding/json"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
)

// AlgebraExpr is one node of a shelf's table-algebra expression: a leaf Field, or
// an Op over Args. Wire form: a leaf is a FieldRef object; an operator node is
// {"op":"cross"|"concat"|"nest","args":[…]}.
type AlgebraExpr struct {
	// Field is the leaf field reference; nil for an operator node.
	Field *FieldRef
	// Op is the operator of an operator node; empty for a leaf.
	Op AlgebraOp
	// Args are the operator's operands, in order.
	Args []*AlgebraExpr
}

// MarshalJSON renders the flattened wire form of the expression.
func (e AlgebraExpr) MarshalJSON() ([]byte, error) {
	w := algebraWire{Op: e.Op, Args: e.Args}
	if e.Field != nil {
		w = algebraWire{Field: e.Field.Name, Agg: e.Field.Agg}
	}

	out, err := json.Marshal(w)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "chartspec: encode shelf")
	}

	return out, nil
}

// UnmarshalJSON decodes the flattened wire form: an object with "field" is a
// leaf, one with "op" an operator node; anything else is CodeInvalidInput.
func (e *AlgebraExpr) UnmarshalJSON(b []byte) error {
	var algebraWireValue algebraWire

	err := json.Unmarshal(b, &algebraWireValue)
	if err != nil {
		return apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"chartspec: invalid shelf expression",
		)
	}

	switch {
	case algebraWireValue.Field != "" && algebraWireValue.Op == "" &&
		len(algebraWireValue.Args) == 0:
		*e = AlgebraExpr{
			Field: &FieldRef{Name: algebraWireValue.Field, Agg: algebraWireValue.Agg},
		}
	case algebraWireValue.Field == "" && algebraWireValue.Op != "":
		*e = AlgebraExpr{Op: algebraWireValue.Op, Args: algebraWireValue.Args}
	default:
		return apperr.New(
			apperr.CodeInvalidInput,
			"chartspec: a shelf node is either a field or an operator",
		)
	}

	return nil
}

// Shelf is a Rows or Columns shelf: a table-algebra expression (nil = empty shelf).
type Shelf = *AlgebraExpr

// Encodings maps a field to each visual channel of the marks.
type Encodings struct {
	// Color colors marks by a field.
	Color *FieldRef `json:"color,omitempty"`
	// Size sizes marks by a field.
	Size *FieldRef `json:"size,omitempty"`
	// Shape shapes marks by a field.
	Shape *FieldRef `json:"shape,omitempty"`
	// Label labels marks with a field.
	Label *FieldRef `json:"label,omitempty"`
}

// Fields returns the encoded field references in channel order (color, size, shape,
// label).
func (e Encodings) Fields() []FieldRef {
	var out []FieldRef

	for _, ref := range []*FieldRef{e.Color, e.Size, e.Shape, e.Label} {
		if ref != nil {
			out = append(out, *ref)
		}
	}

	return out
}

// TimeRange bounds the observations a query reads: From inclusive, To exclusive.
type TimeRange struct {
	// From is the inclusive lower bound.
	From time.Time `json:"from"`
	// To is the exclusive upper bound.
	To time.Time `json:"to"`
}

// ChartSpec is a declarative visualization: shelves of table-algebra expressions
// over one cube's fields, plus detail, encodings, filter, sort, time range, grain
// and an optional explicit mark. foundation/chartspec parses it (gated by the cube's
// allow-list) and compiles it into one AggregateQuery, the pane table and a mark
// per pane.
type ChartSpec struct {
	// TimeRange bounds the observations read.
	TimeRange TimeRange
	// Filter is the parsed listquery predicate (set by chartspec.Parse; nil = no filter).
	Filter Filter
	// Rows is the rows shelf.
	Rows Shelf
	// Columns is the columns shelf.
	Columns Shelf
	// Encodings maps fields to the marks' visual channels.
	Encodings Encodings
	// Cube names the cube queried.
	Cube string
	// Grain is the time-axis resolution.
	Grain Grain
	// Mark, when set, overrides the inferred mark of every pane.
	Mark Mark
	// FilterTree is the filter as a structured tree (wire key "filter", the listquery
	// grammar); chartspec.Parse compiles it into Filter. Nil means no filter.
	FilterTree *FilterNode
	// Detail adds group-by fields that are not on a shelf.
	Detail []FieldRef
	// Sort orders the result rows.
	Sort []OrderField
}

// Wire renders the ChartSpec wire form (the filter tree in the listquery grammar). It is
// the only encoder: plain json.Marshal of a ChartSpec does not produce the wire form.
func (s *ChartSpec) Wire() ([]byte, error) {
	chartSpecWireValue := chartSpecWire{
		TimeRange: s.TimeRange,
		Rows:      s.Rows,
		Columns:   s.Columns,
		Encodings: s.Encodings,
		Cube:      s.Cube,
		Grain:     s.Grain,
		Mark:      s.Mark,
		Filter:    s.FilterTree,
		Detail:    s.Detail,
	}
	for _, o := range s.Sort {
		chartSpecWireValue.Sort = append(
			chartSpecWireValue.Sort,
			sortWire{Field: o.Field, Desc: o.Desc},
		)
	}

	out, err := json.Marshal(chartSpecWireValue)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "chartspec: encode")
	}

	return out, nil
}

// UnmarshalJSON decodes the ChartSpec wire form. It decodes the filter into FilterTree;
// chartspec.Parse validates the whole spec and compiles the filter.
func (s *ChartSpec) UnmarshalJSON(b []byte) error {
	var chartSpecWireValue chartSpecWire

	err := json.Unmarshal(b, &chartSpecWireValue)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInvalidInput, "chartspec: invalid JSON")
	}

	*s = ChartSpec{
		TimeRange:  chartSpecWireValue.TimeRange,
		Rows:       chartSpecWireValue.Rows,
		Columns:    chartSpecWireValue.Columns,
		Encodings:  chartSpecWireValue.Encodings,
		Cube:       chartSpecWireValue.Cube,
		Grain:      chartSpecWireValue.Grain,
		Mark:       chartSpecWireValue.Mark,
		FilterTree: chartSpecWireValue.Filter,
		Detail:     chartSpecWireValue.Detail,
	}
	for _, o := range chartSpecWireValue.Sort {
		s.Sort = append(s.Sort, OrderField{Field: o.Field, Desc: o.Desc})
	}

	return nil
}
