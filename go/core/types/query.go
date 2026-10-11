package types

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
)

// FilterOperator is the comparison a filter clause applies to a field. The values are
// the stable wire tokens (minus the leading "$") that foundation/listquery maps from
// the JSON filter grammar. v1 ships eq/ne/in/not_in/like/gt/gte/lt/lte; between,
// is_null, is_not_null, and not are deferred until a resource needs them.
type FilterOperator string

const (
	// OpEq matches rows where the field equals the value.
	OpEq FilterOperator = "eq"
	// OpNe matches rows where the field does not equal the value.
	OpNe FilterOperator = "ne"
	// OpIn matches rows where the field is one of the values (Value is a slice).
	OpIn FilterOperator = "in"
	// OpNotIn matches rows where the field is none of the values (Value is a slice).
	OpNotIn FilterOperator = "not_in"
	// OpLike matches rows where the field contains the value (case-insensitive substring).
	OpLike FilterOperator = "like"
	// OpGt matches rows where the field is greater than the value.
	OpGt FilterOperator = "gt"
	// OpGte matches rows where the field is greater than or equal to the value.
	OpGte FilterOperator = "gte"
	// OpLt matches rows where the field is less than the value.
	OpLt FilterOperator = "lt"
	// OpLte matches rows where the field is less than or equal to the value.
	OpLte FilterOperator = "lte"
)

// Filter is the parsed, validated list-query filter a store applies as a WHERE
// predicate. It lives here in core/types (not core/interfaces) because CompositeFilter
// — a core/types value that holds []Filter — would otherwise force core/types to
// import core/interfaces, and core/interfaces already imports core/types (a cycle).
//
// sealed union: FilterClause | CompositeFilter (a closed two-implementer set;
// ARCHITECTURE.md#interface-composition)
type Filter interface {
	// IsEmpty reports whether the filter carries no constraint (the store omits WHERE).
	IsEmpty() bool
}

// JoinTarget describes a single-hop, correlated-EXISTS join for a filter clause whose
// column lives on a JOINED table rather than the queried entity's own table. A base row
// matches when a row in Table — correlated by Table.TargetKey = <base>.LocalKey —
// satisfies the clause's operator/value on one of Columns. More than one Column ORs
// across them (a member "name" over first_name + last_name). TargetSoftDeletes adds a
// `Table.deleted_at IS NULL` guard so a match via a soft-deleted target row is excluded.
// It is compiled to `EXISTS (SELECT 1 FROM Table WHERE …)` by the query backend
// (QueryBackend.ExistsClause), so it never joins/duplicates the base query.
type JoinTarget struct {
	// Table is the physical joined table, e.g. "users".
	Table string
	// LocalKey is the FK column on the queried entity's table, e.g. "user_id".
	LocalKey string
	// TargetKey is the joined table's key the LocalKey references, e.g. "id".
	TargetKey string
	// Columns are the target-table column(s) the clause matches; >1 ORs across them.
	Columns []string
	// CaseInsensitive marks the target column's text matching case-insensitive
	// ($like/ILIKE).
	CaseInsensitive bool
	// TargetSoftDeletes adds a `deleted_at IS NULL` guard on the target (set when the
	// target soft-deletes).
	TargetSoftDeletes bool
}

// AggregateSpec describes a correlated-aggregate ORDER BY over a CHILD table: the base
// row sorts by `(SELECT <Func>(<Column|*>) FROM Table WHERE Table.TargetKey =
// <base>.LocalKey [AND Table.deleted_at IS NULL])`. It backs sorting a computed
// count/extremum that is NOT a base column — e.g. a parent's child count (COUNT over its
// child rows) or a group's member count (COUNT over a membership table). Func is "count"
// (Column ignored, COUNT(*)), "max", or "min". TargetSoftDeletes adds the `deleted_at IS
// NULL` guard so the aggregate counts only live children (matching the enrichment the
// list displays). Like a join it never joins the base query, so scope/keyset stay intact;
// an aggregate sort always rides the offset pager (a custom sort).
type AggregateSpec struct {
	// Table is the physical child table aggregated over, e.g. "items".
	Table string
	// LocalKey is the base-row column the child rows correlate to, e.g. "id" or
	// "group_id" (a row whose children live in another table under that id).
	LocalKey string
	// TargetKey is the FK column on Table referencing LocalKey, e.g. "parent_id" /
	// "group_id".
	TargetKey string
	// Func is the aggregate function: "count" (COUNT(*), Column ignored), "max", or "min".
	Func string
	// Column is the child column aggregated by max/min (ignored for count).
	Column string
	// TargetSoftDeletes adds a `Table.deleted_at IS NULL` guard so only live children are
	// aggregated.
	TargetSoftDeletes bool
}

// IntervalCase maps one discriminator value to an added interval, in seconds (a "hourly"
// schedule → 3600). It is a member of IntervalSpec.Cases.
type IntervalCase struct {
	// Value is the discriminator column's DB value this case matches (e.g. "hourly").
	Value string
	// Seconds is the interval added to the base timestamp for this case.
	Seconds int64
}

// IntervalSpec describes a computed timestamp-plus-interval ORDER BY: the base row sorts
// by `BaseColumn + CASE DiscriminatorColumn WHEN v0 THEN interval0 … ELSE NULL END`, so a
// value derived at read time from (timestamp, category) — e.g. a next-run time =
// last_run_at + the schedule's interval — is sortable WITHOUT denormalizing it (the value
// is a pure function of two columns, so a computed expression can never drift, unlike a
// stored copy). A row whose discriminator matches no case, or whose BaseColumn is NULL,
// sorts as NULL. Always a custom sort → the offset pager.
type IntervalSpec struct {
	// BaseColumn is the base timestamp column the interval is added to, e.g. "last_sync_at".
	BaseColumn string
	// DiscriminatorColumn is the category column selecting which interval to add, e.g.
	// "schedule".
	DiscriminatorColumn string
	// Cases maps each discriminator value to its interval (seconds); an unmatched value
	// yields NULL.
	Cases []IntervalCase
}

// DerivedSort is a computed / correlated ORDER BY that is NOT a base column: exactly one
// of Aggregate | Interval is set. It is the sort analogue of a JoinTarget — a store
// stamps it onto a sort field via its derived-sort resolver, KeysetColumns carries it
// (never into the keyset seek — a derived sort is always a custom sort → the offset
// pager), and the backend renders it (QueryBackend.OrderDerived).
type DerivedSort struct {
	// Aggregate, when non-nil, sorts by a correlated aggregate over a child table
	// (counts/extrema).
	Aggregate *AggregateSpec
	// Interval, when non-nil, sorts by a base timestamp plus a category-selected interval.
	Interval *IntervalSpec
}

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
	Field string
	// Join, when non-nil, sorts by a column on a JOINED table instead of Field on the
	// base table: the backend renders ORDER BY over a correlated subquery. Only set for a
	// joined sort field (always a custom sort → the offset pager; keyset stays
	// same-table), stamped by KeysetColumns from the resource's join resolver.
	Join *JoinTarget
	// Derived, when non-nil, sorts by a COMPUTED / CORRELATED expression that is not a
	// base column a correlated aggregate over a child table (a child or member count) or
	// a timestamp-plus-interval (a next-run time). Like Join it is stamped by
	// KeysetColumns from the resource's derived-sort resolver and rendered by the backend
	// (OrderDerived); always a custom sort → the offset pager (keyset stays same-table
	// default order).
	Derived *DerivedSort
	// Ordinal, when non-empty, sorts Field's (same-table) column by a value ORDINAL: the
	// backend renders `ORDER BY CASE Field WHEN v0 THEN 0 … END` so an enum column sorts
	// by meaning, not alphabetically. The values are the column's DB values in ascending
	// order. A custom sort → the offset pager (keyset stays the default order).
	Ordinal []string
	// Desc sorts descending when true, ascending when false.
	Desc bool
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

// KeysetCursor carries the previous page's last-row ordering values for a keyset seek
// The seek predicate pairs these with the resource's ordered keyset columns
// (the caller's sort fields, then the resource tiebreaker column, then id).
//
// A cursor is bound to the ACCESS SCOPE it was minted in (the tenant and access
// restrictions the store's base query applies). The Fingerprint binds only (sort ⊕
// filter), NOT that scope — the scope lives in the store's base query, outside the user
// filter — so a cursor replayed against a different scope (or after the caller's access
// changed) is not rejected; it just seeks from a boundary row that may not belong to the
// new scope, which can dup/skip rows within that scope (never a cross-scope leak: the
// base query still constrains the results). Callers MUST mint a fresh cursor when the
// access scope changes rather than carrying one across scopes.
type KeysetCursor struct {
	// SortValues are the last row's values for each NON-id keyset column, in ORDER BY order
	// (the caller's sort fields then the resource's tiebreaker column, e.g. created_at).
	SortValues []any
	// ID is the last row's id — the always-present final tiebreaker column's value.
	ID string
	// Fingerprint binds the cursor to the (sort ⊕ filter) it was minted under; a mismatch
	// against the current request is rejected so a cursor can't seek an incompatible
	// order. It does NOT bind the access scope (see the type doc) — cursors are
	// scope-bound by contract.
	Fingerprint []byte
}

// Fingerprint returns a stable hash binding a keyset page cursor to the (sort ⊕ filter)
// it was minted under. A keyset cursor carries this fingerprint; on the next request it
// is recomputed and compared, and a mismatch is rejected — so a cursor can never seek
// into an ordering different from the one it was created for (the no-dup/no-skip
// precondition).
//
// The encoding is recursive and ORDER-PRESERVING: reordering the sort fields, or
// reordering the members of a CompositeFilter arm, yields a DIFFERENT fingerprint (a
// semantically- reordered request is a new ordering, mirroring CompileOrder/compileEach,
// which preserve member order). Values are TYPE-TAGGED, so a numeric 5 and a string "5"
// never collide into the same fingerprint (which would let a cursor cross an incompatible
// ordering).
func Fingerprint(sort []OrderField, filter Filter) []byte {
	var b strings.Builder
	b.WriteString("sort:")
	for _, of := range sort {
		b.WriteString(of.Field)
		if of.Desc {
			b.WriteString("|desc;")
		} else {
			b.WriteString("|asc;")
		}
	}
	b.WriteString("|filter:")
	encodeFilterFingerprint(&b, filter)
	sum := sha256.Sum256([]byte(b.String()))
	return sum[:]
}

// encodeFilterFingerprint writes a recursive, order-preserving canonical encoding of a
// filter: a clause as `clause(field|op|typedValue)`, a composite as `and(...)`/`or(...)`
// with each arm encoded in order. An empty/nil filter encodes to `()`.
func encodeFilterFingerprint(b *strings.Builder, f Filter) {
	if f == nil || f.IsEmpty() {
		b.WriteString("()")
		return
	}
	switch v := f.(type) {
	case FilterClause:
		b.WriteString("clause(")
		b.WriteString(v.Field)
		b.WriteByte('|')
		b.WriteString(string(v.Operator))
		b.WriteByte('|')
		encodeValueFingerprint(b, v.Value)
		b.WriteByte(')')
	case CompositeFilter:
		if len(v.And) > 0 {
			b.WriteString("and(")
			for _, sub := range v.And {
				encodeFilterFingerprint(b, sub)
				b.WriteByte(',')
			}
			b.WriteByte(')')
			return
		}
		b.WriteString("or(")
		for _, sub := range v.Or {
			encodeFilterFingerprint(b, sub)
			b.WriteByte(',')
		}
		b.WriteByte(')')
	default:
		b.WriteString("()")
	}
}

// encodeValueFingerprint writes a type-tagged, deterministic encoding of a filter value,
// so a value's Go type is part of the fingerprint (a numeric 5 vs a string "5" hash
// differently) and a slice value (OpIn/OpNotIn) is encoded element-wise in order.
func encodeValueFingerprint(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("nil:")
	case string:
		b.WriteString("str:")
		b.WriteString(x)
	case bool:
		b.WriteString("bool:")
		b.WriteString(strconv.FormatBool(x))
	case []any:
		b.WriteString("list:[")
		for _, e := range x {
			encodeValueFingerprint(b, e)
			b.WriteByte(',')
		}
		b.WriteByte(']')
	default:
		// int/int64/float64/time.Time and other scalars the listquery parser emits: the Go
		// type (%T) tags the value (%v), both deterministic for these scalar types.
		fmt.Fprintf(b, "%T:%v", x, x)
	}
}
