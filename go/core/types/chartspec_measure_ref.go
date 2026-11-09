package types

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
