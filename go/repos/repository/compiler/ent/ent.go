// Package ent is the Ent/SQL backend of the multi-target query-compilation SDK: it
// interprets the list-query IR into Ent dialect/sql predicates and order options — the
// func(*sql.Selector) shape every Ent query accepts via query.Where(predicate.T(pred))
// and query.Order(opt). It is the only backend that imports entgo.io/ent, so the Ent
// dependency stays confined to the repos tier while the shared fold
// (foundation/listquery) and the algebra (core/interfaces) stay ORM-free.
package ent

import (
	"strconv"
	"strings"

	entsql "entgo.io/ent/dialect/sql"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// EntPredicate is the Ent query-node type: a function that mutates a *sql.Selector to add
// a WHERE predicate or an ORDER BY term. Ent unifies filter and sort as this one shape,
// so the backend uses it for both the predicate type Q and the sort type S of the
// QueryBackend algebra.
type EntPredicate = func(*entsql.Selector)

// Backend interprets the list-query IR into parameterized Ent predicates and order
// options. It is stateless — construct it with New — and satisfies
// interfaces.QueryBackend[EntPredicate, EntPredicate].
type Backend struct{}

// Ensure Backend implements the query-compiler algebra for the Ent/SQL target.
var _ interfaces.QueryBackend[EntPredicate, EntPredicate] = Backend{}

// New returns the Ent query-compiler backend.
func New() Backend { return Backend{} }

// noop is the match-all predicate: it adds no WHERE clause.
func noop(*entsql.Selector) {}

// Empty is the no-op (match-all) predicate for an absent or empty filter.
func (Backend) Empty() EntPredicate { return noop }

// Clause interprets a single leaf comparison into a parameterized Ent predicate. The
// column comes only from the allow-list (never client input) and the value is always a
// bound parameter, so the result is injection-safe. Values arrive already type-validated
// by the listquery parser; a value whose Go type does not fit the operator (only
// reachable from a hand-built clause) degrades to a no-op predicate rather than
// panicking.
func (Backend) Clause(col string, op types.FilterOperator, v any) EntPredicate {
	switch op {
	case types.OpEq:
		return entsql.FieldEQ(col, v)
	case types.OpNe:
		return entsql.FieldNEQ(col, v)
	case types.OpGt:
		return entsql.FieldGT(col, v)
	case types.OpGte:
		return entsql.FieldGTE(col, v)
	case types.OpLt:
		return entsql.FieldLT(col, v)
	case types.OpLte:
		return entsql.FieldLTE(col, v)
	case types.OpLike:
		s, ok := v.(string)
		if !ok {
			return noop
		}
		return entsql.FieldContainsFold(col, s)
	case types.OpIn:
		vs, ok := v.([]any)
		if !ok {
			return noop
		}
		return entsql.FieldIn(col, vs...)
	case types.OpNotIn:
		vs, ok := v.([]any)
		if !ok {
			return noop
		}
		return entsql.FieldNotIn(col, vs...)
	default:
		return noop
	}
}

// ExistsClause interprets a leaf comparison on a JOINED table's column into a correlated
// EXISTS predicate: the base row matches when a row in join.Table — correlated by
// join.TargetKey = <base>.join.LocalKey — satisfies op/v on one of join.Columns (ORed
// when >1). It adds NO join to the base query, so the base scope/soft-delete predicates
// and keyset order are untouched; when join.TargetSoftDeletes is set the subquery also
// excludes soft-deleted target rows. A $like match folds case when join.CaseInsensitive
// is set (ILIKE via ContainsFold) and is case-sensitive otherwise. A value whose Go type
// does not fit the operator degrades to a match-all (add-nothing) predicate, mirroring
// Clause.
func (Backend) ExistsClause(
	join types.JoinTarget,
	op types.FilterOperator,
	v any,
) EntPredicate {
	return func(s *entsql.Selector) {
		target := entsql.Table(join.Table)
		colPreds := make([]*entsql.Predicate, 0, len(join.Columns))
		for _, col := range join.Columns {
			if p := colPredicate(target.C(col), op, v, join.CaseInsensitive); p != nil {
				colPreds = append(colPreds, p)
			}
		}
		if len(colPreds) == 0 {
			// A malformed/no-column value ⇒ match-all (add no predicate), mirroring
			// Clause's noop.
			return
		}
		valuePred := colPreds[0]
		if len(colPreds) > 1 {
			// Multiple target columns (a member "name" over first_name + last_name) OR together.
			valuePred = entsql.Or(colPreds...)
		}
		conds := []*entsql.Predicate{
			// Correlate the subquery to the base row: target.<target_key> = <base>.<local_key>.
			entsql.ColumnsEQ(target.C(join.TargetKey), s.C(join.LocalKey)),
			valuePred,
		}
		if join.TargetSoftDeletes {
			// Don't match via a soft-deleted target row (mirrors the base soft-delete
			// interceptor).
			conds = append(conds, entsql.IsNull(target.C("deleted_at")))
		}
		sub := entsql.Select(target.C(join.TargetKey)).
			From(target).
			Where(entsql.And(conds...))
		s.Where(entsql.Exists(sub))
	}
}

// colPredicate builds the parameterized *Predicate for one (already-qualified) column +
// operator + value — used inside the correlated EXISTS subquery. It mirrors Clause's
// operator set with the value-level entsql helpers (so multiple target columns can be
// OR-composed) and returns nil for a value whose Go type does not fit the operator, so
// the caller degrades to match-all rather than panic. For $like, caseInsensitive selects
// case-folding matching (ILIKE via ContainsFold) vs case-sensitive (Contains), honoring
// the join's CaseInsensitive flag.
func colPredicate(
	col string,
	op types.FilterOperator,
	v any,
	caseInsensitive bool,
) *entsql.Predicate {
	switch op {
	case types.OpEq:
		return entsql.EQ(col, v)
	case types.OpNe:
		return entsql.NEQ(col, v)
	case types.OpGt:
		return entsql.GT(col, v)
	case types.OpGte:
		return entsql.GTE(col, v)
	case types.OpLt:
		return entsql.LT(col, v)
	case types.OpLte:
		return entsql.LTE(col, v)
	case types.OpLike:
		s, ok := v.(string)
		if !ok {
			return nil
		}
		if caseInsensitive {
			return entsql.ContainsFold(col, s)
		}
		return entsql.Contains(col, s)
	case types.OpIn:
		vs, ok := v.([]any)
		if !ok {
			return nil
		}
		return entsql.In(col, vs...)
	case types.OpNotIn:
		vs, ok := v.([]any)
		if !ok {
			return nil
		}
		return entsql.NotIn(col, vs...)
	default:
		return nil
	}
}

// And combines sub-predicates so that every one must match, via sql.AndPredicates — the
// combinator over the func(*sql.Selector) predicate shape (sql.And operates on
// *sql.Predicate, a different type, so it cannot compose these).
func (Backend) And(
	subs []EntPredicate,
) EntPredicate {
	return entsql.AndPredicates(subs...)
}

// Or combines sub-predicates so that at least one must match, via sql.OrPredicates.
func (Backend) Or(
	subs []EntPredicate,
) EntPredicate {
	return entsql.OrPredicates(subs...)
}

// Order interprets one sort directive into an Ent order option (ORDER BY col ASC/DESC) as
// a func(*sql.Selector), the shape query.Order accepts.
func (Backend) Order(col string, desc bool) EntPredicate {
	dir := entsql.OrderAsc()
	if desc {
		dir = entsql.OrderDesc()
	}
	return entsql.OrderByField(col, dir).ToFunc()
}

// OrderJoin interprets a joined sort directive into an ORDER BY over a correlated scalar
// subquery per join column: `ORDER BY (SELECT t.col FROM t WHERE t.target_key =
// base.local_key [AND t.deleted_at IS NULL] LIMIT 1) <dir> NULLS <LAST|FIRST>`. All joins
// are N:1 with a non-null FK, so the subquery yields one row — and NULL only when the
// sole target row is soft-deleted: the join.TargetSoftDeletes guard excludes it
// (mirroring ExistsClause/OrderDerived), so a soft-deleted target never contributes a
// STALE value to the sort and instead sorts as NULL, hence the explicit, direction-aware
// NULLS clause. Without the guard the filter (ExistsClause) and the sort would be
// asymmetric on the same soft-deleted target. A multi-column join (member "name") emits
// one ORDER BY term per column (first_name, then last_name); the correlated PK lookup is
// cheap (EXPLAIN evidence), so the doubled lookup is acceptable and a single LATERAL is
// not required. It never joins the base query.
func (Backend) OrderJoin(join types.JoinTarget, desc bool) EntPredicate {
	// Direction + explicit NULLS placement: NULLS LAST for ascending, NULLS FIRST for
	// descending, so a soft-deleted-target row (the only NULL case) sorts predictably
	// regardless of dialect defaults.
	tail := " ASC NULLS LAST"
	if desc {
		tail = " DESC NULLS FIRST"
	}
	return func(s *entsql.Selector) {
		for _, col := range join.Columns {
			target := entsql.Table(join.Table)
			conds := []*entsql.Predicate{
				entsql.ColumnsEQ(target.C(join.TargetKey), s.C(join.LocalKey)),
			}
			if join.TargetSoftDeletes {
				// Exclude a soft-deleted target so the sort never reads its stale value —
				// the sort matches ExistsClause's filter on the same target (a
				// soft-deleted target → NULL → NULLS LAST/FIRST), rather than sorting a
				// soft-deleted row among the live ones.
				conds = append(conds, entsql.IsNull(target.C("deleted_at")))
			}
			sub := entsql.Select(target.C(col)).
				From(target).
				Where(entsql.And(conds...)).
				Limit(1)
			s.OrderExprFunc(func(b *entsql.Builder) {
				b.WriteByte('(')
				b.Join(sub)
				b.WriteByte(')')
				b.WriteString(tail)
			})
		}
	}
}

// OrderOrdinal interprets a value-ordinal sort directive into `ORDER BY CASE col WHEN
// 'v0' THEN 0 WHEN 'v1' THEN 1 … ELSE <len> END <dir>`, so an enum column sorts by the
// declared value order (e.g. access_level admin<write<read = owner<editor<viewer) rather
// than alphabetically. An unlisted value sorts last (the ELSE <len> bucket). Empty values
// falls back to a plain column order (defensive).
//
// The values are inlined as quoted SQL literals (with ” escaping), NOT bound parameters:
// they are TRUSTED proto DATA from the (listquery.field).column.sort_ordinal annotation —
// never client input, exactly like the column name — so inlining is safe, and it avoids
// the placeholder-numbering collision that arises when an ORDER BY expression's bound
// args interleave with the WHERE clause's args.
func (Backend) OrderOrdinal(col string, values []string, desc bool) EntPredicate {
	if len(values) == 0 {
		return (Backend{}).Order(col, desc)
	}
	return func(s *entsql.Selector) {
		s.OrderExprFunc(func(b *entsql.Builder) {
			b.WriteString("CASE ")
			b.WriteString(s.C(col))
			for i, v := range values {
				b.WriteString(" WHEN '")
				b.WriteString(strings.ReplaceAll(v, "'", "''"))
				b.WriteString("' THEN ")
				b.WriteString(strconv.Itoa(i))
			}
			b.WriteString(" ELSE ")
			b.WriteString(strconv.Itoa(len(values)))
			b.WriteString(" END")
			if desc {
				b.WriteString(" DESC")
			} else {
				b.WriteString(" ASC")
			}
		})
	}
}

// OrderDerived interprets a COMPUTED / CORRELATED sort directive into an ORDER BY over an
// expression that is not a base column — exactly one arm of d is set:
//
//   - d.Aggregate: a correlated aggregate over a child table, `ORDER BY (SELECT COUNT(*)
//     FROM child WHERE child.target_key = base.local_key [AND child.deleted_at IS NULL])
//     <dir>` (or MAX/MIN of a column). It backs sorting a computed count that is not a
//     base column — a connector's Documents count, a team grant's Members count — and
//     never joins the base query, exactly like OrderJoin.
//   - d.Interval: a timestamp-plus-interval, `ORDER BY (base.base_col + CASE
//     base.discriminator WHEN 'v0' THEN make_interval(secs=>n0) … ELSE NULL END) <dir>` —
//     a connector's Next Sync = last_sync_at
//   - the schedule's interval, computed in SQL so it can never drift from a stored copy.
//
// NULLS placement is direction-aware (LAST asc / FIRST desc) so a NULL derived value (a
// soft-deleted-only aggregate is 0, but an unmatched interval case or a NULL base
// timestamp yields NULL) sorts predictably. The interval seconds + discriminator values
// are TRUSTED spec DATA (from the store's derived-sort resolver, never client input, like
// a column name), so they are inlined as SQL literals — the same safety argument as
// OrderOrdinal. A malformed spec (neither arm set) degrades to a no-op sort node.
func (Backend) OrderDerived(d types.DerivedSort, desc bool) EntPredicate {
	tail := " ASC NULLS LAST"
	if desc {
		tail = " DESC NULLS FIRST"
	}
	switch {
	case d.Aggregate != nil:
		agg := *d.Aggregate
		target := entsql.Table(agg.Table)
		var selExpr string
		switch agg.Func {
		case "max":
			selExpr = entsql.Max(target.C(agg.Column))
		case "min":
			selExpr = entsql.Min(target.C(agg.Column))
		default: // "count"
			selExpr = entsql.Count("*")
		}
		return func(s *entsql.Selector) {
			conds := []*entsql.Predicate{
				entsql.ColumnsEQ(target.C(agg.TargetKey), s.C(agg.LocalKey)),
			}
			if agg.TargetSoftDeletes {
				conds = append(conds, entsql.IsNull(target.C("deleted_at")))
			}
			sub := entsql.Select(selExpr).From(target).Where(entsql.And(conds...))
			s.OrderExprFunc(func(b *entsql.Builder) {
				b.WriteByte('(')
				b.Join(sub)
				b.WriteByte(')')
				b.WriteString(tail)
			})
		}
	case d.Interval != nil:
		iv := *d.Interval
		return func(s *entsql.Selector) {
			s.OrderExprFunc(func(b *entsql.Builder) {
				b.WriteByte('(')
				b.WriteString(s.C(iv.BaseColumn))
				b.WriteString(" + CASE ")
				b.WriteString(s.C(iv.DiscriminatorColumn))
				for _, c := range iv.Cases {
					b.WriteString(" WHEN '")
					b.WriteString(strings.ReplaceAll(c.Value, "'", "''"))
					b.WriteString("' THEN make_interval(secs => ")
					b.WriteString(strconv.FormatInt(c.Seconds, 10))
					b.WriteByte(')')
				}
				b.WriteString(" ELSE NULL END)")
				b.WriteString(tail)
			})
		}
	default:
		return noop
	}
}
