package ent

// The folds below are the Ent glue shared by every app's ListRunner adapters: they fold a
// resolved-column ListSpec's predicates and ORDER BY into Ent query-node functions
// through the Backend. The per-entity adapters (which apply these to their typed
// *ent.XQuery and execute) live in each app's stores because Ent has no generic query
// interface; only this backend-folding is app-agnostic.

import (
	entsql "entgo.io/ent/dialect/sql"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
)

// FoldFilters folds resolved-column filters to Ent predicates with an identity resolver —
// a ListSpec's filters (user filter + keyset seek) already carry resolved columns, so no
// allow-list is consulted.
func FoldFilters(filters []types.Filter) []func(*entsql.Selector) {
	b := New()
	out := make([]func(*entsql.Selector), len(filters))

	for i, f := range filters {
		out[i] = listquery.CompileSeek(b, f)
	}

	return out
}

// FoldOrder folds the resolved ORDER BY columns to Ent sort options via the same backend.
// A joined sort field carries a JoinTarget instead of a base column, rendered as an ORDER
// BY over a correlated subquery; a derived field carries an aggregate/interval spec,
// rendered as a computed expression; an ordinal field renders as a CASE over its values;
// a same-table field renders as a plain ORDER BY.
func FoldOrder(order []types.OrderField) []func(*entsql.Selector) {
	backend := New()
	out := make([]func(*entsql.Selector), len(order))

	for index, orderField := range order {
		switch {
		case orderField.Join != nil:
			out[index] = backend.OrderJoin(orderField.Join, orderField.Desc)
		case orderField.Derived != nil:
			// A derived sort renders as an ORDER BY over a correlated aggregate / interval expr.
			out[index] = backend.OrderDerived(*orderField.Derived, orderField.Desc)
		case len(orderField.Ordinal) > 0:
			// A value-ordinal sort renders as an ORDER BY CASE over the column's values.
			out[index] = backend.OrderOrdinal(
				orderField.Field,
				orderField.Ordinal,
				orderField.Desc,
			)
		default:
			out[index] = backend.Order(orderField.Field, orderField.Desc)
		}
	}

	return out
}
