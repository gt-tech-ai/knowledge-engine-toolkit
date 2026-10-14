package ent

import (
	entsql "entgo.io/ent/dialect/sql"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
)

// Apply compiles a validated DSL filter + sort into the Ent selector-level predicate and
// order-option funcs, resolving each field's storage column via the resource's allow-list
// (fmap). It is the shared convenience every wired list store uses: the returned
// predicate is AND-ed into the store's authz-scoped Ent query, and the order funcs
// (carrying the resource keyset column then id as tiebreakers) are converted to the
// entity's OrderOption and spread into .Order(...). tiebreaker is the resource's keyset
// column (from ResourceSchema.KeysetColumn — created_at for most resources, joined_at for
// a membership join table); an empty tiebreaker falls back to created_at. An empty filter
// compiles to a no-op predicate.
func Apply(
	fmap *listquery.Map,
	filter types.Filter,
	sort []types.OrderField,
	tiebreaker string,
) (predicate func(*entsql.Selector), order []func(*entsql.Selector)) {
	b := New()
	predicate = listquery.Compile(b, fmap.Column, filter)
	// A derived sort (aggregate/interval) is store-provided (Reader.ResolveDerived), not
	// declared in the allow-list Map, so this Map-driven path carries none.
	order = listquery.CompileOrder(
		b, fmap.Column, fmap.Join, fmap.Ordinal, nil, sort, tiebreaker,
	)
	return predicate, order
}
