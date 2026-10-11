package types

import (
	"encoding/json"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// AlgebraOp is a table-algebra operator combining shelf expressions.
type AlgebraOp string

const (
	// AlgebraCross is the Cartesian product of the operands' tuples (×).
	AlgebraCross AlgebraOp = "cross"
	// AlgebraConcat is the union of the operands' tuples (+).
	AlgebraConcat AlgebraOp = "concat"
	// AlgebraNest is the product restricted to combinations present in the data (/).
	AlgebraNest AlgebraOp = "nest"
)

// Aggregate is the aggregation applied to a measure field; the empty value marks
// a dimension (no aggregation).
type Aggregate string

const (
	// AggNone marks a field used as a dimension.
	AggNone Aggregate = ""
	// AggSum is the sum of the measure.
	AggSum Aggregate = "sum"
	// AggAvg is the arithmetic mean of the measure.
	AggAvg Aggregate = "avg"
	// AggMin is the smallest value of the measure.
	AggMin Aggregate = "min"
	// AggMax is the largest value of the measure.
	AggMax Aggregate = "max"
	// AggCount is the number of observations.
	AggCount Aggregate = "count"
	// AggCountDistinct is the number of distinct values.
	AggCountDistinct Aggregate = "count_distinct"
	// AggP50 is the median (from a mergeable sketch).
	AggP50 Aggregate = "p50"
	// AggP95 is the 95th percentile (from a mergeable sketch).
	AggP95 Aggregate = "p95"
	// AggP99 is the 99th percentile (from a mergeable sketch).
	AggP99 Aggregate = "p99"
)

// Grain is the time resolution a query buckets its time axis to.
type Grain string

const (
	// GrainMinute buckets by minute.
	GrainMinute Grain = "minute"
	// GrainHour buckets by hour.
	GrainHour Grain = "hour"
	// GrainDay buckets by UTC day.
	GrainDay Grain = "day"
	// GrainMonth buckets by UTC month.
	GrainMonth Grain = "month"
)

// Mark is how a pane is drawn; the empty value lets the compiler infer it.
type Mark string

const (
	// MarkAuto lets the compiler infer the mark from the pane's field kinds.
	MarkAuto Mark = ""
	// MarkText draws a text table (dimension × dimension).
	MarkText Mark = "text"
	// MarkBar draws bars (dimension × measure).
	MarkBar Mark = "bar"
	// MarkLine draws a line (time × measure).
	MarkLine Mark = "line"
	// MarkPoint draws a scatter (measure × measure).
	MarkPoint Mark = "point"
)

// FieldRef references one cube field on a shelf; Agg is set for a measure and empty for a
// dimension. Wire form: {"field":"team"} or {"field":"tokens_in","agg":"sum"}.
type FieldRef struct {
	// Name is the cube field name.
	Name string `json:"field"`
	// Agg is the aggregation for a measure; empty for a dimension.
	Agg Aggregate `json:"agg,omitempty"`
}

// Key renders the reference as a stable label: "team" or "sum(tokens_in)".
func (r FieldRef) Key() string {
	if r.Agg == AggNone {
		return r.Name
	}
	return string(r.Agg) + "(" + r.Name + ")"
}

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

// algebraWire is the JSON shape of an AlgebraExpr (leaf and operator fields flattened).
type algebraWire struct {
	// Op is the operator of an operator node.
	Op AlgebraOp `json:"op,omitempty"`
	// Field is the leaf field name.
	Field string `json:"field,omitempty"`
	// Agg is the leaf's aggregation.
	Agg Aggregate `json:"agg,omitempty"`
	// Args are the operator's operands.
	Args []*AlgebraExpr `json:"args,omitempty"`
}

// MarshalJSON renders the flattened wire form of the expression.
func (e *AlgebraExpr) MarshalJSON() ([]byte, error) {
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
	var w algebraWire
	if err := json.Unmarshal(b, &w); err != nil {
		return apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"chartspec: invalid shelf expression",
		)
	}
	switch {
	case w.Field != "" && w.Op == "" && len(w.Args) == 0:
		*e = AlgebraExpr{Field: &FieldRef{Name: w.Field, Agg: w.Agg}}
	case w.Field == "" && w.Op != "":
		*e = AlgebraExpr{Op: w.Op, Args: w.Args}
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
	// FilterJSON is the filter's wire form (the listquery JSON grammar), kept so the
	// spec re-serializes exactly; chartspec.Parse compiles it into Filter.
	FilterJSON json.RawMessage
	// Detail adds group-by fields that are not on a shelf.
	Detail []FieldRef
	// Sort orders the result rows.
	Sort []OrderField
}

// sortWire is the JSON shape of one sort directive.
type sortWire struct {
	// Field is the field (or "agg(field)" key) to sort by.
	Field string `json:"field"`
	// Desc sorts descending.
	Desc bool `json:"desc,omitempty"`
}

// chartSpecWire is the JSON shape of a ChartSpec.
type chartSpecWire struct {
	// TimeRange bounds the observations read.
	TimeRange TimeRange `json:"time_range"`
	// Rows is the rows shelf.
	Rows *AlgebraExpr `json:"rows,omitempty"`
	// Columns is the columns shelf.
	Columns *AlgebraExpr `json:"columns,omitempty"`
	// Encodings maps fields to channels.
	Encodings Encodings `json:"encodings"`
	// Cube names the cube.
	Cube string `json:"cube"`
	// Grain is the time resolution.
	Grain Grain `json:"grain,omitempty"`
	// Mark is the explicit mark.
	Mark Mark `json:"mark,omitempty"`
	// Filter is the listquery JSON filter.
	Filter json.RawMessage `json:"filter,omitempty"`
	// Detail adds group-by fields.
	Detail []FieldRef `json:"detail,omitempty"`
	// Sort orders the rows.
	Sort []sortWire `json:"sort,omitempty"`
}

// MarshalJSON renders the ChartSpec wire form (the filter as its kept FilterJSON).
func (s ChartSpec) MarshalJSON() ([]byte, error) {
	w := chartSpecWire{
		TimeRange: s.TimeRange,
		Rows:      s.Rows,
		Columns:   s.Columns,
		Encodings: s.Encodings,
		Cube:      s.Cube,
		Grain:     s.Grain,
		Mark:      s.Mark,
		Filter:    s.FilterJSON,
		Detail:    s.Detail,
	}
	for _, o := range s.Sort {
		w.Sort = append(w.Sort, sortWire{Field: o.Field, Desc: o.Desc})
	}
	out, err := json.Marshal(w)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "chartspec: encode")
	}
	return out, nil
}

// UnmarshalJSON decodes the ChartSpec wire form. It keeps the filter as FilterJSON;
// chartspec.Parse validates the whole spec and compiles the filter.
func (s *ChartSpec) UnmarshalJSON(b []byte) error {
	var w chartSpecWire
	if err := json.Unmarshal(b, &w); err != nil {
		return apperr.Wrap(err, apperr.CodeInvalidInput, "chartspec: invalid JSON")
	}
	*s = ChartSpec{
		TimeRange:  w.TimeRange,
		Rows:       w.Rows,
		Columns:    w.Columns,
		Encodings:  w.Encodings,
		Cube:       w.Cube,
		Grain:      w.Grain,
		Mark:       w.Mark,
		FilterJSON: w.Filter,
		Detail:     w.Detail,
	}
	for _, o := range w.Sort {
		s.Sort = append(s.Sort, OrderField{Field: o.Field, Desc: o.Desc})
	}
	return nil
}

// MeasureRef is one measure an aggregate query computes: Name aggregated by Agg.
type MeasureRef struct {
	// Name is the measure field.
	Name string
	// Agg is the aggregation applied.
	Agg Aggregate
}

// AggregateQuery is the single store query a ChartSpec compiles to: group the
// cube's facts for OrgID in TimeRange by GroupBy (and the Grain time bucket),
// compute Measures, apply Filter, order by Sort, and return at most Limit rows
// (0 = unbounded).
type AggregateQuery struct {
	// TimeRange bounds the observations read.
	TimeRange TimeRange
	// Filter is the predicate applied (nil = none).
	Filter Filter
	// Cube names the cube.
	Cube string
	// OrgID is the tenant; always the leading partition key.
	OrgID string
	// Grain is the time-bucket resolution.
	Grain Grain
	// GroupBy are the dimension fields grouped by, in order.
	GroupBy []string
	// Measures are the aggregates computed, in order.
	Measures []MeasureRef
	// Sort orders the result rows.
	Sort []OrderField
	// ResumeToken, when set, resumes a previous stream after its last delivered
	// page (RowStream.ResumeToken).
	ResumeToken []byte
	// Limit caps the rows returned (0 = no cap).
	Limit int
}
