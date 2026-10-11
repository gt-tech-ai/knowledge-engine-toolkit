package listquery

import (
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// ColumnFn resolves an allow-listed DTO field name to its storage column. Build one from
// a resource's filter allow-list with (*Map).Column; it falls back to the field name when
// the field is not allow-listed.
type ColumnFn func(field string) string

// JoinFn resolves an allow-listed DTO field name to its joined-table target, or nil for a
// same-table field. Build one from a resource's allow-list with (*Map).Join. A nil JoinFn
// (a resource with no joined fields) is treated as "no joins" by ResolveFilter.
type JoinFn func(field string) *types.JoinTarget

// OrdinalFn resolves an allow-listed DTO field name to its value-ordinal sort order — the
// column's DB values in ascending order — or nil for a field with no ordinal (plain
// lexical sort). Build one from a resource's allow-list with (*Map).Ordinal. A nil
// OrdinalFn is treated as "no ordinals".
type OrdinalFn func(field string) []string

// DerivedFn resolves a sort field name to a COMPUTED / CORRELATED sort spec — a
// correlated aggregate (a Documents/Members count) or a timestamp-plus-interval (a
// connector's Next Sync) — or nil for a field whose sort is a plain/joined/ordinal
// column. Unlike JoinFn/OrdinalFn (built from the generated allow-list Map), a DerivedFn
// is supplied by the STORE, because a derived sort's correlation (which child table,
// which interval) is repos-tier knowledge, not a proto-declared column mapping. A nil
// DerivedFn (a resource with no derived sort fields) is treated as "no derived sorts".
type DerivedFn func(field string) *types.DerivedSort

// IdentityColumn is a ColumnFn that returns its input unchanged. It is used to compile
// a keyset SeekFilter, whose clauses already carry resolved storage columns (from
// KeysetColumns), so no further field→column mapping is wanted. Pass it as a value
// (Resolve: listquery.IdentityColumn) wherever a ColumnFn is expected.
func IdentityColumn(field string) string { return field }

// CompileSeek folds a keyset SeekFilter (whose clauses carry already-resolved columns)
// into a backend predicate through the SAME QueryBackend algebra the user filter uses. A
// store ANDs the result into its scoped query, so the seek is not a special ORM artifact
// — just another filter.
func CompileSeek[Q, S any](b interfaces.QueryBackend[Q, S], seek types.Filter) Q {
	return Compile(b, IdentityColumn, seek)
}

// ResolveFilter rewrites a filter's DTO field names to their storage columns via resolve,
// and stamps each clause's joined-table target via joinOf (nil ⇒ no joins), returning a
// filter whose clauses carry resolved columns + join targets. It lets the pagination
// template resolve the user filter ONCE, up front, so the datastore ListRunner sees only
// resolved clauses and folds every predicate (user filter + keyset seek) with an identity
// resolver — the runner never needs the allow-list. A joined clause keeps its
// (unused-for-the-predicate) resolved Field for IsEmpty, and carries the JoinTarget the
// backend compiles to a correlated EXISTS.
func ResolveFilter(resolve ColumnFn, joinOf JoinFn, f types.Filter) types.Filter {
	if f == nil || f.IsEmpty() {
		return f
	}
	switch v := f.(type) {
	case types.FilterClause:
		var join *types.JoinTarget
		if joinOf != nil {
			join = joinOf(v.Field)
		}
		return types.FilterClause{
			Field:    resolve(v.Field),
			Operator: v.Operator,
			Value:    v.Value,
			Join:     join,
		}
	case types.CompositeFilter:
		if len(v.And) > 0 {
			return types.CompositeFilter{And: resolveEach(resolve, joinOf, v.And)}
		}
		return types.CompositeFilter{Or: resolveEach(resolve, joinOf, v.Or)}
	default:
		return f
	}
}

// resolveEach resolves each sub-filter's fields + join targets, preserving member order.
func resolveEach(resolve ColumnFn, joinOf JoinFn, subs []types.Filter) []types.Filter {
	out := make([]types.Filter, len(subs))
	for i, sub := range subs {
		out[i] = ResolveFilter(resolve, joinOf, sub)
	}
	return out
}

// Tiebreaker columns appended to every compiled ORDER BY for a fully-deterministic total
// order. tiebreakerCreatedAt is the default primary time tiebreaker; a resource whose
// keyset column is not created_at (e.g. a membership join table keyed on joined_at)
// passes its own via CompileOrder. tiebreakerID is the always-appended secondary
// tiebreaker: every entity compiled through this SDK carries a unique id column. Keyset
// pagination needs this total ordering to avoid page-boundary duplicate/skip.
const (
	tiebreakerCreatedAt = "created_at" // default primary time tiebreaker column
	tiebreakerID        = "id"         // always-appended secondary tiebreaker column
)

// KeysetIDColumn is the always-present final tiebreaker column — every entity compiled
// through this SDK carries a unique id. The keyset seek compiler pairs the cursor's id
// value with this column (and is exported so the repos-tier compiler need not restate the
// literal).
const KeysetIDColumn = tiebreakerID

// Compile folds a validated Filter (the FilterClause | CompositeFilter sealed union) into
// a backend predicate Q through the backend algebra b, resolving each clause's DTO field
// to its storage column with resolve. A nil or empty filter compiles to b.Empty() (the
// no-op / match-all). Compile imports no ORM — every datastore specific lives in b — so
// it is the single, shared fold every backend reuses.
//
// Compile trusts that f was produced by the listquery parser (its values already
// type-validated); a backend defensively guards a hand-built malformed value rather than
// re-validating here.
func Compile[Q, S any](
	b interfaces.QueryBackend[Q, S],
	resolve ColumnFn,
	f types.Filter,
) Q {
	if f == nil || f.IsEmpty() {
		return b.Empty()
	}
	switch v := f.(type) {
	case types.FilterClause:
		// A clause carrying a JoinTarget (stamped by ResolveFilter) compiles to a
		// correlated EXISTS on the joined table; a same-table clause compiles to a column
		// predicate as before.
		if v.Join != nil {
			return b.ExistsClause(*v.Join, v.Operator, v.Value)
		}
		return b.Clause(resolve(v.Field), v.Operator, v.Value)
	case types.CompositeFilter:
		// The parser populates exactly one arm ($and or $or); prefer And when present.
		if len(v.And) > 0 {
			return b.And(compileEach(b, resolve, v.And))
		}
		return b.Or(compileEach(b, resolve, v.Or))
	default:
		return b.Empty()
	}
}

// compileEach folds each sub-filter to a predicate, preserving member order.
func compileEach[Q, S any](
	b interfaces.QueryBackend[Q, S],
	resolve ColumnFn,
	subs []types.Filter,
) []Q {
	out := make([]Q, 0, len(subs))
	for _, sub := range subs {
		out = append(out, Compile(b, resolve, sub))
	}
	return out
}

// KeysetColumn is one column of the fully-deterministic keyset order: the resolved
// storage column and its sort direction. The ORDER BY (CompileOrder) and the keyset seek
// predicate (SeekFilter) are BOTH built from the same KeysetColumns sequence, so the
// WHERE seek can never drift from the ORDER BY it must mirror — the no-dup/no-skip
// precondition.
type KeysetColumn struct {
	// Column is the resolved storage column (empty when Join is set — a joined sort has
	// no base column).
	Column string
	// Join, when non-nil, is a joined-table sort target: the ORDER BY is a correlated
	// subquery, not a base column. Only the caller's sort fields can be joined; the
	// appended tiebreaker + id are always same-table. A joined column never participates
	// in the keyset seek (a joined sort is always a custom sort → the offset pager), so
	// this is consumed only by the ORDER BY path.
	Join *types.JoinTarget
	// Derived, when non-nil, is a COMPUTED / CORRELATED sort — a correlated aggregate or
	// a timestamp-plus-interval — with no base column (Column is empty). Like Join, only
	// a caller sort field carries it (never a tiebreaker), and it rides the offset pager
	// (never the keyset seek).
	Derived *types.DerivedSort
	// Ordinal, when non-empty, sorts Column by a value ordinal instead of lexically — the
	// column's DB values in ascending order. Like Join, only a caller sort field carries
	// it (never a tiebreaker), and it rides the offset pager (never the keyset seek).
	Ordinal []string
	// Desc is true for a descending column, false for ascending.
	Desc bool
}

// KeysetColumns returns the fully-deterministic ordered (column, direction) sequence a
// list query both orders by AND seeks on: the caller's resolved sort fields in their
// directions, then the resource's tiebreaker column DESC (created_at by default, or the
// resource's own keyset column, e.g. joined_at for a membership join table), then id DESC
// — with a column dedup (a tiebreaker the caller already sorts by, in either direction,
// is not re-appended). An empty tiebreaker falls back to created_at. It is the SINGLE
// source of the total order: CompileOrder renders it to ORDER BY nodes and the keyset
// seek compiler renders it to the WHERE predicate, so the two cannot drift. Precondition:
// the entity carries the tiebreaker column and a unique id column.
func KeysetColumns(
	resolve ColumnFn,
	joinOf JoinFn,
	ordinalOf OrdinalFn,
	derivedOf DerivedFn,
	ofs []types.OrderField,
	tiebreaker string,
) []KeysetColumn {
	if tiebreaker == "" {
		tiebreaker = tiebreakerCreatedAt
	}
	out := make([]KeysetColumn, 0, len(ofs)+2)
	seen := make(map[string]bool, len(ofs)+2)
	appendCol := func(col string, desc bool) {
		if seen[col] {
			return
		}
		seen[col] = true
		out = append(out, KeysetColumn{Column: col, Desc: desc})
	}
	for _, of := range ofs {
		// A joined sort field carries a JoinTarget instead of a base column; the ORDER BY
		// is a correlated subquery, so it never participates in the keyset seek (a joined
		// sort is always a custom sort → the offset pager). Dedup it by a join signature
		// (never collides with a same-table tiebreaker, which is always a base column).
		if joinOf != nil {
			if j := joinOf(of.Field); j != nil {
				key := "\x00join\x00" + j.Table + "\x00" + strings.Join(j.Columns, ",")
				if !seen[key] {
					seen[key] = true
					out = append(out, KeysetColumn{Join: j, Desc: of.Desc})
				}
				continue
			}
		}
		// A derived sort field carries a correlated-aggregate / interval spec instead of
		// a base column (a Documents/Members count, a Next Sync). Like a join it never
		// participates in the keyset seek (always a custom sort → the offset pager).
		// Dedup by the resolved field name (a derived sort has no base column to key on;
		// the field name is unique per resolver).
		if derivedOf != nil {
			if d := derivedOf(of.Field); d != nil {
				key := "\x00derived\x00" + of.Field
				if !seen[key] {
					seen[key] = true
					out = append(out, KeysetColumn{Derived: d, Desc: of.Desc})
				}
				continue
			}
		}
		// A same-table field with a value ordinal sorts by a CASE over its column; it
		// dedups on the resolved column like a plain column (an ordinal is just a
		// different ORDER BY over that column).
		col := resolve(of.Field)
		if ordinalOf != nil {
			if ord := ordinalOf(of.Field); len(ord) > 0 {
				if !seen[col] {
					seen[col] = true
					out = append(
						out,
						KeysetColumn{Column: col, Ordinal: ord, Desc: of.Desc},
					)
				}
				continue
			}
		}
		appendCol(col, of.Desc)
	}
	appendCol(tiebreaker, true)
	appendCol(tiebreakerID, true)
	return out
}

// CompileOrder folds the ordered sort directives into backend sort nodes over the shared
// KeysetColumns sequence (caller sort fields → tiebreaker DESC → id DESC, deduped) — so
// the emitted ORDER BY and the keyset seek predicate are guaranteed to agree. See
// KeysetColumns for the tiebreaker + dedup semantics.
func CompileOrder[Q, S any](
	b interfaces.QueryBackend[Q, S],
	resolve ColumnFn,
	joinOf JoinFn,
	ordinalOf OrdinalFn,
	derivedOf DerivedFn,
	ofs []types.OrderField,
	tiebreaker string,
) []S {
	cols := KeysetColumns(resolve, joinOf, ordinalOf, derivedOf, ofs, tiebreaker)
	out := make([]S, 0, len(cols))
	for _, kc := range cols {
		// A joined sort column renders as an ORDER BY over a correlated subquery; a
		// derived column as an aggregate/interval expression; an ordinal column as a CASE
		// over its values; a plain same-table column (incl. every tiebreaker) as a plain
		// ORDER BY.
		switch {
		case kc.Join != nil:
			out = append(out, b.OrderJoin(*kc.Join, kc.Desc))
		case kc.Derived != nil:
			out = append(out, b.OrderDerived(*kc.Derived, kc.Desc))
		case len(kc.Ordinal) > 0:
			out = append(out, b.OrderOrdinal(kc.Column, kc.Ordinal, kc.Desc))
		default:
			out = append(out, b.Order(kc.Column, kc.Desc))
		}
	}
	return out
}
