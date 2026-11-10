package types

// IntervalSpec describes a computed timestamp-plus-interval ORDER BY: the base row sorts
// by `BaseColumn + CASE DiscriminatorColumn WHEN v0 THEN interval0 … ELSE NULL END`, so a
// value derived at read time from (timestamp, category) — e.g. a next-run time =
// last_run_at + the schedule's interval — is sortable WITHOUT denormalizing it (the value
// is a pure function of two columns, so a computed expression can never drift, unlike a
// stored copy). A row whose discriminator matches no case, or whose BaseColumn is NULL,
// sorts as NULL. Always a custom sort → the offset pager.
type IntervalSpec struct {
	// BaseColumn is the base timestamp column the interval is added to, e.g. "last_sync_at".
	BaseColumn string `json:"base_column"`
	// DiscriminatorColumn is the category column selecting which interval to add, e.g.
	// "schedule".
	DiscriminatorColumn string `json:"discriminator_column"`
	// Cases maps each discriminator value to its interval (seconds); an unmatched value
	// yields NULL.
	Cases []IntervalCase `json:"cases"`
}

// DerivedSort is a computed / correlated ORDER BY that is NOT a base column: exactly one
// of Aggregate | Interval is set. It is the sort analogue of a JoinTarget — a store
// stamps it onto a sort field via its derived-sort resolver, KeysetColumns carries it
// (never into the keyset seek — a derived sort is always a custom sort → the offset
// pager), and the backend renders it (QueryBackend.OrderDerived).
type DerivedSort struct {
	// Aggregate, when non-nil, sorts by a correlated aggregate over a child table
	// (counts/extrema).
	Aggregate *AggregateSpec `json:"aggregate"`
	// Interval, when non-nil, sorts by a base timestamp plus a category-selected interval.
	Interval *IntervalSpec `json:"interval"`
}
