// Package ent is the Ent kind of the store tier (store.KindEnt): the Ent/SQL backend of
// the list-query compiler — it interprets the list-query IR into Ent dialect/sql
// predicates and order options, the func(*sql.Selector) shape every Ent query accepts
// via query.Where(predicate.T(pred)) and query.Order(opt) — plus Apply and the
// FoldFilters/FoldOrder glue an Ent store's list adapters share. It is the only package
// that imports entgo.io/ent, so the Ent dependency stays confined to this kind while the
// shared fold (foundation/listquery) and the algebra (core/interfaces) stay ORM-free.
package ent

import (
	"strconv"
	"strings"

	entsql "entgo.io/ent/dialect/sql"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Predicate is the Ent query-node type: a function that mutates a *sql.Selector to add
// a WHERE predicate or an ORDER BY term. Ent unifies filter and sort as this one shape,
// so the backend uses it for both the predicate type Q and the sort type S of the
// QueryBackend algebra.
type Predicate = func(*entsql.Selector)

// Backend interprets the list-query IR into parameterized Ent predicates and order
// options. It is stateless — construct it with New — and satisfies
// interfaces.QueryBackend[EntPredicate, EntPredicate].
type Backend struct{}

// Ensure Backend implements the query-compiler algebra for the Ent/SQL target.
var _ interfaces.QueryBackend[Predicate, Predicate] = Backend{}

// New returns the Ent query-compiler backend.
func New() Backend { return Backend{} }

// noop is the match-all predicate: it adds no WHERE clause.
func noop(*entsql.Selector) {}

// Empty is the no-op (match-all) predicate for an absent or empty filter.
func (Backend) Empty() Predicate { return noop }

// Clause interprets a single leaf comparison into a parameterized Ent predicate. The
// column comes only from the allow-list (never client input) and the value is always a
// bound parameter, so the result is injection-safe. Values arrive already type-validated
// by the listquery parser; a value whose Go type does not fit the operator (only
// reachable from a hand-built clause) degrades to a no-op predicate rather than
// panicking.
func (Backend) Clause(col string, op types.FilterOperator, value any) Predicate {
	switch op {
	case types.OpEq, types.OpNe, types.OpGt, types.OpGte, types.OpLt, types.OpLte:
		return compareClause(col, op, value)
	case types.OpLike:
		return likeClause(col, value)
	case types.OpIn:
		return inClause(col, value)
	case types.OpNotIn:
		return notInClause(col, value)
	default:
		return noop
	}
}

// compareClause builds a comparison predicate on a base column.
func compareClause(col string, operation types.FilterOperator, value any) Predicate {
	if operation == types.OpEq {
		return entsql.FieldEQ(col, value)
	}

	if operation == types.OpNe {
		return entsql.FieldNEQ(col, value)
	}

	if operation == types.OpGt {
		return entsql.FieldGT(col, value)
	}

	if operation == types.OpGte {
		return entsql.FieldGTE(col, value)
	}

	if operation == types.OpLt {
		return entsql.FieldLT(col, value)
	}

	return entsql.FieldLTE(col, value)
}

// likeClause matches a substring, case-insensitively; a non-string value matches all.
func likeClause(col string, v any) Predicate {
	s, ok := v.(string)
	if !ok {
		return noop
	}

	return entsql.FieldContainsFold(col, s)
}

// inClause matches a list; a non-list value matches all.
func inClause(col string, v any) Predicate {
	vs, ok := v.([]any)
	if !ok {
		return noop
	}

	return entsql.FieldIn(col, vs...)
}

// notInClause excludes a list; a non-list value matches all.
func notInClause(col string, v any) Predicate {
	vs, ok := v.([]any)
	if !ok {
		return noop
	}

	return entsql.FieldNotIn(col, vs...)
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
// join must not be nil.
func (Backend) ExistsClause(
	joinRef *types.JoinTarget,
	op types.FilterOperator,
	v any,
) Predicate {
	join := *joinRef

	return func(s *entsql.Selector) { applyExists(s, &join, op, v) }
}

// applyExists adds the correlated EXISTS over the join target. A malformed/no-column
// value matches all (adds no predicate), mirroring Clause's noop.
func applyExists(
	selector *entsql.Selector, join *types.JoinTarget, op types.FilterOperator, v any,
) {
	target := entsql.Table(join.Table)

	valuePred := anyColumnPredicate(target, join, op, v)
	if valuePred == nil {
		return
	}

	conds := []*entsql.Predicate{
		// Correlate the subquery to the base row: target.<target_key> = <base>.<local_key>.
		entsql.ColumnsEQ(target.C(join.TargetKey), selector.C(join.LocalKey)),
		valuePred,
	}
	if join.TargetSoftDeletes {
		// Don't match via a soft-deleted target row (mirrors the base soft-delete
		// interceptor).
		conds = append(conds, entsql.IsNull(target.C(textDeletedAt)))
	}

	sub := entsql.Select(target.C(join.TargetKey)).
		From(target).
		Where(entsql.And(conds...))
	selector.Where(entsql.Exists(sub))
}

// anyColumnPredicate builds the value predicate: multiple target columns (a member
// "name" over first_name + last_name) OR together. nil means no column applies.
func anyColumnPredicate(
	target *entsql.SelectTable, join *types.JoinTarget, op types.FilterOperator, v any,
) *entsql.Predicate {
	preds := make([]*entsql.Predicate, 0, len(join.Columns))

	for _, col := range join.Columns {
		if p := colPredicate(target.C(col), op, v, join); p != nil {
			preds = append(preds, p)
		}
	}

	switch len(preds) {
	case 0:
		return nil
	case 1:
		return preds[0]
	default:
		return entsql.Or(preds...)
	}
}

// colPredicate builds the parameterized *Predicate for one (already-qualified) column +
// operator + value — used inside the correlated EXISTS subquery. It mirrors Clause's
// operator set with the value-level entsql helpers (so multiple target columns can be
// OR-composed) and returns nil for a value whose Go type does not fit the operator, so
// the caller degrades to match-all rather than panic. For $like, join selects
// case-folding matching (ILIKE via ContainsFold) vs case-sensitive (Contains), honoring
// the join's CaseInsensitive flag.
func colPredicate(
	col string,
	operation types.FilterOperator,
	value any,
	join *types.JoinTarget,
) *entsql.Predicate {
	switch operation {
	case types.OpEq, types.OpNe, types.OpGt, types.OpGte, types.OpLt, types.OpLte:
		return comparePredicate(col, operation, value)
	case types.OpLike:
		return likePredicate(col, value, join)
	case types.OpIn, types.OpNotIn:
		return listPredicate(col, operation, value)
	default:
		return nil
	}
}

// comparePredicate builds a comparison predicate on a qualified column.
func comparePredicate(
	col string,
	operation types.FilterOperator,
	value any,
) *entsql.Predicate {
	if operation == types.OpEq {
		return entsql.EQ(col, value)
	}

	if operation == types.OpNe {
		return entsql.NEQ(col, value)
	}

	if operation == types.OpGt {
		return entsql.GT(col, value)
	}

	if operation == types.OpGte {
		return entsql.GTE(col, value)
	}

	if operation == types.OpLt {
		return entsql.LT(col, value)
	}

	return entsql.LTE(col, value)
}

// likePredicate matches a substring; a non-string value yields nil.
func likePredicate(
	col string,
	v any,
	join *types.JoinTarget,
) *entsql.Predicate {
	text, ok := v.(string)
	if !ok {
		return nil
	}

	if join.CaseInsensitive {
		return entsql.ContainsFold(col, text)
	}

	return entsql.Contains(col, text)
}

// listPredicate matches or excludes a list; a non-list value yields nil.
func listPredicate(col string, operation types.FilterOperator, v any) *entsql.Predicate {
	items, ok := v.([]any)
	if !ok {
		return nil
	}

	if operation == types.OpIn {
		return entsql.In(col, items...)
	}

	return entsql.NotIn(col, items...)
}

// And combines sub-predicates so that every one must match, via sql.AndPredicates — the
// combinator over the func(*sql.Selector) predicate shape (sql.And operates on
// *sql.Predicate, a different type, so it cannot compose these).
func (Backend) And(
	subs []Predicate,
) Predicate {
	return entsql.AndPredicates(subs...)
}

// Or combines sub-predicates so that at least one must match, via sql.OrPredicates.
func (Backend) Or(
	subs []Predicate,
) Predicate {
	return entsql.OrPredicates(subs...)
}

// Order interprets one sort directive into an Ent order option (ORDER BY col ASC/DESC) as
// a func(*sql.Selector), the shape query.Order accepts.
func (Backend) Order(col string, desc bool) Predicate {
	dir := map[bool]entsql.OrderTermOption{
		false: entsql.OrderAsc(),
		true:  entsql.OrderDesc(),
	}[desc]

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
// join must not be nil.
func (Backend) OrderJoin(joinRef *types.JoinTarget, desc bool) Predicate {
	join := *joinRef

	tail := nullsLastTail(desc)

	return func(s *entsql.Selector) {
		for _, col := range join.Columns {
			orderByJoinColumn(s, &join, col, tail)
		}
	}
}

// nullsLastTail is the direction with explicit NULLS placement: NULLS LAST ascending,
// NULLS FIRST descending, so a NULL sorts predictably regardless of dialect defaults.
func nullsLastTail(desc bool) string {
	return map[bool]string{false: " ASC NULLS LAST", true: " DESC NULLS FIRST"}[desc]
}

// orderByJoinColumn orders by a correlated subquery over one target column. A
// soft-deleted target is excluded so the sort never reads its stale value, matching
// ExistsClause's filter on the same target.
func orderByJoinColumn(
	selector *entsql.Selector,
	join *types.JoinTarget,
	col, tail string,
) {
	target := entsql.Table(join.Table)
	conds := []*entsql.Predicate{
		entsql.ColumnsEQ(target.C(join.TargetKey), selector.C(join.LocalKey)),
	}

	if join.TargetSoftDeletes {
		conds = append(conds, entsql.IsNull(target.C(textDeletedAt)))
	}

	sub := entsql.Select(target.C(col)).
		From(target).
		Where(entsql.And(conds...)).
		Limit(1)

	orderBySubquery(selector, sub, tail)
}

// orderBySubquery appends `(subquery) tail` to the ORDER BY.
func orderBySubquery(s, sub *entsql.Selector, tail string) {
	s.OrderExprFunc(func(b *entsql.Builder) {
		b.WriteByte('(')
		b.Join(sub)
		b.WriteByte(')')
		b.WriteString(tail)
	})
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
func (Backend) OrderOrdinal(col string, values []string, desc bool) Predicate {
	if len(values) == 0 {
		return (Backend{}).Order(col, desc)
	}

	return func(s *entsql.Selector) {
		s.OrderExprFunc(
			func(b *entsql.Builder) { writeOrdinalCase(b, s.C(col), values, desc) },
		)
	}
}

// writeOrdinalCase writes a CASE ranking col by its position in values.
func writeOrdinalCase(
	builder *entsql.Builder,
	col string,
	values []string,
	desc bool,
) {
	builder.WriteString("CASE ")
	builder.WriteString(col)

	for i, v := range values {
		builder.WriteString(" WHEN '")
		builder.WriteString(strings.ReplaceAll(v, "'", "''"))
		builder.WriteString("' THEN ")
		builder.WriteString(strconv.Itoa(i))
	}

	builder.WriteString(" ELSE ")
	builder.WriteString(strconv.Itoa(len(values)))
	builder.WriteString(" END")

	builder.WriteString(map[bool]string{false: " ASC", true: " DESC"}[desc])
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
func (Backend) OrderDerived(derivedSort types.DerivedSort, desc bool) Predicate {
	tail := nullsLastTail(desc)

	switch {
	case derivedSort.Aggregate != nil:
		return orderByAggregate(derivedSort.Aggregate, tail)
	case derivedSort.Interval != nil:
		return orderByInterval(*derivedSort.Interval, tail)
	default:
		return noop
	}
}

// orderByAggregate orders by a correlated aggregate over the target table.
func orderByAggregate(aggRef *types.AggregateSpec, tail string) Predicate {
	agg := *aggRef

	target := entsql.Table(agg.Table)
	selExpr := aggregateExpr(target, &agg)

	return func(selector *entsql.Selector) {
		conds := []*entsql.Predicate{
			entsql.ColumnsEQ(target.C(agg.TargetKey), selector.C(agg.LocalKey)),
		}
		if agg.TargetSoftDeletes {
			conds = append(conds, entsql.IsNull(target.C(textDeletedAt)))
		}

		orderBySubquery(
			selector,
			entsql.Select(selExpr).From(target).Where(entsql.And(conds...)),
			tail,
		)
	}
}

// aggregateExpr renders the aggregate's select expression; any other func counts.
func aggregateExpr(target *entsql.SelectTable, agg *types.AggregateSpec) string {
	switch agg.Func {
	case "max":
		return entsql.Max(target.C(agg.Column))
	case "min":
		return entsql.Min(target.C(agg.Column))
	default:
		return entsql.Count("*")
	}
}

// orderByInterval orders by a base column plus a per-discriminator interval.
func orderByInterval(iv types.IntervalSpec, tail string) Predicate {
	return func(s *entsql.Selector) {
		s.OrderExprFunc(func(b *entsql.Builder) { writeIntervalCase(b, s, &iv, tail) })
	}
}

// writeIntervalCase writes `(base + CASE discriminator WHEN ... END) tail`.
func writeIntervalCase(
	builder *entsql.Builder,
	selector *entsql.Selector,
	intervalSpec *types.IntervalSpec,
	tail string,
) {
	builder.WriteByte('(')
	builder.WriteString(selector.C(intervalSpec.BaseColumn))
	builder.WriteString(" + CASE ")
	builder.WriteString(selector.C(intervalSpec.DiscriminatorColumn))

	for _, c := range intervalSpec.Cases {
		builder.WriteString(" WHEN '")
		builder.WriteString(strings.ReplaceAll(c.Value, "'", "''"))
		builder.WriteString("' THEN make_interval(secs => ")
		builder.WriteString(strconv.FormatInt(c.Seconds, base10))
		builder.WriteByte(')')
	}

	builder.WriteString(" ELSE NULL END)")
	builder.WriteString(tail)
}
