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

// Known reports whether op is one of the operators above. Stores trust a parsed filter's
// operator, so every parser rejects an unknown one, the empty operator included.
func (op FilterOperator) Known() bool {
	switch op {
	case OpEq, OpNe, OpIn, OpNotIn, OpLike, OpGt, OpGte, OpLt, OpLte:
		return true
	default:
		return false
	}
}

// Filter is the parsed, validated list-query filter a store applies as a WHERE
// predicate. It lives here in core/types (not core/interfaces) because CompositeFilter
// — a core/types value that holds []Filter — would otherwise force core/types to
// import core/interfaces, and core/interfaces already imports core/types (a cycle).
//
// sealed union: FilterClause | CompositeFilter (a closed two-implementer set;
// ARCHITECTURE.md#interface-composition).
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
	Table string `json:"table"`
	// LocalKey is the FK column on the queried entity's table, e.g. "user_id".
	LocalKey string `json:"local_key"`
	// TargetKey is the joined table's key the LocalKey references, e.g. "id".
	TargetKey string `json:"target_key"`
	// Columns are the target-table column(s) the clause matches; >1 ORs across them.
	Columns []string `json:"columns"`
	// CaseInsensitive marks the target column's text matching case-insensitive
	// ($like/ILIKE).
	CaseInsensitive bool `json:"case_insensitive"`
	// TargetSoftDeletes adds a `deleted_at IS NULL` guard on the target (set when the
	// target soft-deletes).
	TargetSoftDeletes bool `json:"target_soft_deletes"`
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
	Table string `json:"table"`
	// LocalKey is the base-row column the child rows correlate to, e.g. "id" or
	// "group_id" (a row whose children live in another table under that id).
	LocalKey string `json:"local_key"`
	// TargetKey is the FK column on Table referencing LocalKey, e.g. "parent_id" /
	// "group_id".
	TargetKey string `json:"target_key"`
	// Func is the aggregate function: "count" (COUNT(*), Column ignored), "max", or "min".
	Func string `json:"func"`
	// Column is the child column aggregated by max/min (ignored for count).
	Column string `json:"column"`
	// TargetSoftDeletes adds a `Table.deleted_at IS NULL` guard so only live children are
	// aggregated.
	TargetSoftDeletes bool `json:"target_soft_deletes"`
}

// IntervalCase maps one discriminator value to an added interval, in seconds (a "hourly"
// schedule → 3600). It is a member of IntervalSpec.Cases.
type IntervalCase struct {
	// Value is the discriminator column's DB value this case matches (e.g. "hourly").
	Value string `json:"value"`
	// Seconds is the interval added to the base timestamp for this case.
	Seconds int64 `json:"seconds"`
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
	var builder strings.Builder

	_, _ = builder.WriteString("sort:")

	for _, of := range sort {
		_, _ = builder.WriteString(of.Field)

		if of.Desc {
			_, _ = builder.WriteString("|desc;")
		} else {
			_, _ = builder.WriteString("|asc;")
		}
	}

	_, _ = builder.WriteString("|filter:")
	encodeFilterFingerprint(&builder, filter)

	sum := sha256.Sum256([]byte(builder.String()))

	return sum[:]
}

// encodeFilterFingerprint writes a recursive, order-preserving canonical encoding of a
// filter: a clause as `clause(field|op|typedValue)`, a composite as `and(...)`/`or(...)`
// with each arm encoded in order. An empty/nil filter encodes to `()`.
func encodeFilterFingerprint(builder *strings.Builder, filter Filter) {
	if filter == nil || filter.IsEmpty() {
		_, _ = builder.WriteString("()")

		return
	}

	switch filterClause := filter.(type) {
	case FilterClause:
		_, _ = builder.WriteString("clause(")
		_, _ = builder.WriteString(filterClause.Field)
		_ = builder.WriteByte('|')
		_, _ = builder.WriteString(string(filterClause.Operator))
		_ = builder.WriteByte('|')
		encodeValueFingerprint(builder, filterClause.Value)

		_ = builder.WriteByte(')')
	case CompositeFilter:
		if len(filterClause.And) > 0 {
			encodeGroupFingerprint(builder, "and(", filterClause.And)

			return
		}

		encodeGroupFingerprint(builder, "or(", filterClause.Or)
	default:
		_, _ = builder.WriteString("()")
	}
}

// encodeGroupFingerprint writes a composite's opening token and its members.
func encodeGroupFingerprint(builder *strings.Builder, open string, members []Filter) {
	_, _ = builder.WriteString(open)

	for _, sub := range members {
		encodeFilterFingerprint(builder, sub)

		_ = builder.WriteByte(',')
	}

	_ = builder.WriteByte(')')
}

// encodeValueFingerprint writes a type-tagged, deterministic encoding of a filter value,
// so a value's Go type is part of the fingerprint (a numeric 5 vs a string "5" hash
// differently) and a slice value (OpIn/OpNotIn) is encoded element-wise in order.
func encodeValueFingerprint(builder *strings.Builder, v any) {
	switch value := v.(type) {
	case nil:
		_, _ = builder.WriteString("nil:")
	case string:
		_, _ = builder.WriteString("str:")
		_, _ = builder.WriteString(value)
	case bool:
		_, _ = builder.WriteString("bool:")
		_, _ = builder.WriteString(strconv.FormatBool(value))
	case []any:
		_, _ = builder.WriteString("list:[")

		for _, e := range value {
			encodeValueFingerprint(builder, e)

			_ = builder.WriteByte(',')
		}

		_ = builder.WriteByte(']')
	default:
		// int/int64/float64/time.Time and other scalars the listquery parser emits: the Go
		// type (%T) tags the value (%v), both deterministic for these scalar types.
		fmt.Fprintf(builder, "%T:%v", value, value)
	}
}
