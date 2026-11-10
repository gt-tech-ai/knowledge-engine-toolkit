package types

// MeasureRef is one measure an aggregate query computes: Name aggregated by Agg.
type MeasureRef struct {
	// Name is the measure field.
	Name string `json:"name"`
	// Agg is the aggregation applied.
	Agg Aggregate `json:"agg"`
}

// AggregateQuery is the single store query a ChartSpec compiles to: group the
// cube's facts for OrgID in TimeRange by GroupBy (and the Grain time bucket),
// compute Measures, apply Filter, order by Sort, and return at most Limit rows
// (0 = unbounded).
type AggregateQuery struct {
	// TimeRange bounds the observations read.
	TimeRange TimeRange `json:"time_range"`
	// Filter is the predicate applied (nil = none).
	Filter Filter `json:"filter"`
	// Cube names the cube.
	Cube string `json:"cube"`
	// OrgID is the tenant; always the leading partition key.
	OrgID string `json:"org_id"`
	// Grain is the time-bucket resolution.
	Grain Grain `json:"grain"`
	// GroupBy are the dimension fields grouped by, in order.
	GroupBy []string `json:"group_by"`
	// Measures are the aggregates computed, in order.
	Measures []MeasureRef `json:"measures"`
	// Sort orders the result rows.
	Sort []OrderField `json:"sort"`
	// ResumeToken, when set, resumes a previous stream after its last delivered
	// page (RowStream.ResumeToken).
	ResumeToken []byte `json:"resume_token"`
	// Limit caps the rows returned (0 = no cap).
	Limit int `json:"limit"`
}
