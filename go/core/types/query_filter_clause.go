package types

// FilterClause is a single field comparison: Field <Operator> Value. For OpIn/OpNotIn,
// Value is a slice of the field's element type; otherwise it is a scalar. (Field order
// is govet fieldalignment-optimal, not the semantic Field/Operator/Value reading.)
type FilterClause struct {
	// Field is the DTO field name (allow-listed + mapped to a storage column by listquery).
	Field string
	// Value is the comparison operand — a scalar, or a slice for OpIn/OpNotIn.
	Value any
	// Join, when non-nil, compiles this clause to a correlated EXISTS on a joined table
	// instead of a same-table predicate on Field; the operator + value still apply,
	// against Join.Columns.
	Join *JoinTarget
	// Operator is the comparison to apply.
	Operator FilterOperator
}

// IsEmpty reports whether the clause carries no field to constrain on.
func (c FilterClause) IsEmpty() bool { return c.Field == "" }

// CompositeFilter is a boolean combination of sub-filters: And requires every member to
// match, Or requires at least one. A composite may nest other composites.
type CompositeFilter struct {
	// And holds sub-filters combined with logical AND.
	And []Filter
	// Or holds sub-filters combined with logical OR.
	Or []Filter
}

// IsEmpty reports whether the composite holds no sub-filters in either arm.
func (c CompositeFilter) IsEmpty() bool { return len(c.And) == 0 && len(c.Or) == 0 }

// OrderField is a validated, allow-listed sort directive: sort by Field, descending when
// Desc. The listquery order allow-list yields it (defaulting to created_at for a
// non-allow-listed field); the repos-tier compiler renders it to an Ent order option.
type OrderField struct {
	// Field is the allow-listed field name to sort by.
	Field string `json:"field"`
	// Join, when non-nil, sorts by a column on a JOINED table instead of Field on the
	// base table: the backend renders ORDER BY over a correlated subquery. Only set for a
	// joined sort field (always a custom sort → the offset pager; keyset stays
	// same-table), stamped by KeysetColumns from the resource's join resolver.
	Join *JoinTarget `json:"join"`
	// Derived, when non-nil, sorts by a COMPUTED / CORRELATED expression that is not a
	// base column a correlated aggregate over a child table (a child or member count) or
	// a timestamp-plus-interval (a next-run time). Like Join it is stamped by
	// KeysetColumns from the resource's derived-sort resolver and rendered by the backend
	// (OrderDerived); always a custom sort → the offset pager (keyset stays same-table
	// default order).
	Derived *DerivedSort `json:"derived"`
	// Ordinal, when non-empty, sorts Field's (same-table) column by a value ORDINAL: the
	// backend renders `ORDER BY CASE Field WHEN v0 THEN 0 … END` so an enum column sorts
	// by meaning, not alphabetically. The values are the column's DB values in ascending
	// order. A custom sort → the offset pager (keyset stays the default order).
	Ordinal []string `json:"ordinal"`
	// Desc sorts descending when true, ascending when false.
	Desc bool `json:"desc"`
}

// ListSpec is a fully-RESOLVED list query command: the WHERE predicates, the ORDER BY,
// and the window, all expressed over STORAGE COLUMNS (not DTO field names). It is the
// interface between the pagination template (foundation/listquery.Run) and a datastore's
// ListRunner: Run composes it (user filter resolved + the keyset seek, the keyset-column
// order, the limit/offset) and the runner folds it into the concrete query and executes.
// Because every field is already resolved, a runner folds it with an identity resolver —
// it never needs the resource's allow-list.
type ListSpec struct {
	// Filters are the resolved-column predicates AND-ed into the query (the user filter,
	// plus the keyset seek on the keyset arm). An empty slice is match-all.
	Filters []Filter

	// Order is the resolved-column ORDER BY sequence (the caller's sort columns, then the
	// resource tiebreaker column, then id) — Field carries the storage column, not a DTO
	// field name.
	Order []OrderField

	// Limit is the maximum number of rows to fetch (the caller passes limit+1 to detect
	// has-more).
	Limit int

	// Offset is the row offset (offset arm); it is 0 on the keyset arm, whose window is
	// the seek.
	Offset int
}

// PageToken is the decoded pagination cursor: a tagged union of a 1-based offset page and
// a keyset seek cursor. When Keyset is non-nil the token is a keyset cursor (infinite
// scroll); otherwise OffsetPage selects the numeric pager. It is the semantic form of the
// opaque base64 token carried in the pagination page_token string — foundation/listquery
// encodes/decodes the two (a JSON payload, base64-wrapped), keeping core/ free of any
// transport dependency.
type PageToken struct {
	// Keyset is the keyset seek cursor when non-nil (infinite scroll).
	Keyset *KeysetCursor
	// OffsetPage is the 1-based page number for the numeric pager (used when Keyset is nil).
	OffsetPage int32
}
