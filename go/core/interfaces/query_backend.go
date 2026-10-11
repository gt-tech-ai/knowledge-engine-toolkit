package interfaces

import "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"

// QueryBackend interprets the list-query IR (types.Filter / types.OrderField) into a
// concrete datastore query. It is the "algebra" of the multi-target query-compilation
// SDK: the shared fold in foundation/listquery walks the IR and calls these methods, and
// each datastore provides exactly one implementation (Ent now; Mongo/Elasticsearch/GORM
// future).
//
// Q is the backend's predicate node type (e.g. Ent's func(*sql.Selector), Mongo's
// bson.M); S is its sort node type. Q and S are separate type parameters because a filter
// and a sort are not the same structure in every datastore: Ent unifies both as
// func(*sql.Selector), but Mongo is a bson.M filter vs a bson.D sort, and Elasticsearch a
// query fragment vs a sort array. Keeping them distinct means the first non-SQL backend
// can model its sort natively without a breaking change to this core interface.
type QueryBackend[Q, S any] interface {
	// Clause interprets a single leaf comparison — storage column <op> value — into a
	// predicate. col is already resolved from the allow-list; v is the parser-validated
	// operand (a scalar, or a []any for OpIn/OpNotIn).
	Clause(col string, op types.FilterOperator, v any) Q

	// ExistsClause interprets a leaf comparison whose column lives on a JOINED table into
	// a correlated EXISTS predicate: the base row matches when a row in join.Table
	// (correlated by join.TargetKey = <base>.join.LocalKey) satisfies op/v on one of
	// join.Columns (ORed when >1). It never joins the base query, so
	// scope/soft-delete/keyset are untouched. v is the parser-validated operand (a
	// scalar, or a []any for OpIn/OpNotIn), same as Clause.
	ExistsClause(join types.JoinTarget, op types.FilterOperator, v any) Q

	// And combines sub-predicates so that every one must match.
	And(subs []Q) Q

	// Or combines sub-predicates so that at least one must match.
	Or(subs []Q) Q

	// Empty is the no-op (match-all) predicate for an absent or empty filter.
	Empty() Q

	// Order interprets one sort directive — storage column, descending when desc — into a
	// sort node.
	Order(col string, desc bool) S

	// OrderJoin interprets a sort directive whose column lives on a JOINED table into a
	// sort node that orders by a correlated subquery over join.Table (correlated by
	// join.TargetKey = <base>.join.LocalKey), one ORDER BY term per join.Columns entry (a
	// member "name" over first_name+last_name). Like ExistsClause it never joins the base
	// query. Joined sorts always ride the offset pager (keyset stays same-table), so no
	// keyset-seek counterpart is needed.
	OrderJoin(join types.JoinTarget, desc bool) S

	// OrderOrdinal interprets a sort directive with a value ORDINAL into a sort node that
	// orders col by the given values' positions (`CASE col WHEN v0 THEN 0 … ELSE len
	// END`), so an enum column sorts by meaning (owner<editor<viewer) rather than
	// alphabetically. values are the column's DB values in ascending order. An ordinal
	// sort is a custom sort (the offset pager); no keyset seek.
	OrderOrdinal(col string, values []string, desc bool) S

	// OrderDerived interprets a COMPUTED / CORRELATED sort directive into a sort node: a
	// correlated aggregate over a child table (d.Aggregate — a Documents/Members COUNT)
	// or a timestamp-plus-interval (d.Interval — a connector's Next Sync = last_sync_at +
	// the schedule's interval). Exactly one arm of d is set. Like OrderJoin it renders an
	// ORDER BY over an expression / correlated subquery and never joins the base query; a
	// derived sort is a custom sort (the offset pager), so there is no keyset-seek
	// counterpart.
	OrderDerived(d types.DerivedSort, desc bool) S
}
