// Package entlist is the Ent glue shared by every app's ListRunner adapters: it folds a
// resolved-column ListSpec's predicates and ORDER BY into Ent query-node functions
// through the entcompiler QueryBackend. The per-entity adapters (which apply these to
// their typed *ent.XQuery and execute) live in each app's stores package because Ent has
// no generic query interface; only this backend-folding is app-agnostic, so it lives once
// here rather than duplicated per app module.
package entlist

import (
	entsql "entgo.io/ent/dialect/sql"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
	entcompiler "github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository/compiler/ent"
)

// FoldFilters folds resolved-column filters to Ent predicates with an identity resolver —
// a ListSpec's filters (user filter + keyset seek) already carry resolved columns, so no
// allow-list is consulted.
func FoldFilters(filters []types.Filter) []func(*entsql.Selector) {
	b := entcompiler.New()
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
	b := entcompiler.New()
	out := make([]func(*entsql.Selector), len(order))
	for i, o := range order {
		switch {
		case o.Join != nil:
			out[i] = b.OrderJoin(*o.Join, o.Desc)
		case o.Derived != nil:
			// A derived sort renders as an ORDER BY over a correlated aggregate / interval expr.
			out[i] = b.OrderDerived(*o.Derived, o.Desc)
		case len(o.Ordinal) > 0:
			// A value-ordinal sort renders as an ORDER BY CASE over the column's values.
			out[i] = b.OrderOrdinal(o.Field, o.Ordinal, o.Desc)
		default:
			out[i] = b.Order(o.Field, o.Desc)
		}
	}
	return out
}
