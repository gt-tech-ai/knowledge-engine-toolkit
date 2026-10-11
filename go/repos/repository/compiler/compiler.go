// Package compiler selects the query-compiler backend for a configured Kind. The Kind
// vocabulary is shared across the SDK; New maps a Kind to the SQL-family (Ent) backend
// the list stores inject. A non-SQL target (Mongo/Elasticsearch/GORM) will add its own
// typed selector, because the heterogeneous backend node types (func(*sql.Selector) vs
// bson.M) cannot share one return type — selection is therefore compositional at the
// root, not a single polymorphic factory.
package compiler

import (
	entsql "entgo.io/ent/dialect/sql"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
	entbackend "github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository/compiler/ent"
)

// Apply compiles a validated DSL filter + sort into the Ent selector-level predicate and
// order-option funcs, resolving each field's storage column via the resource's allow-list
// (fmap). It is the shared convenience every wired list store uses: the returned
// predicate is AND-ed into the store's authz-scoped Ent query, and the order funcs
// (carrying the resource keyset column then id as tiebreakers) are converted to the
// entity's OrderOption and spread into .Order(...). tiebreaker is the resource's keyset
// column (from ResourceSchema.KeysetColumn — created_at for most resources, joined_at for
// a membership join table); an empty tiebreaker falls back to created_at. An empty filter
// compiles to a no-op predicate. It uses the Ent/SQL backend directly (the only
// implemented target today).
func Apply(
	fmap *listquery.Map,
	filter types.Filter,
	sort []types.OrderField,
	tiebreaker string,
) (predicate func(*entsql.Selector), order []func(*entsql.Selector)) {
	b := entbackend.New()
	return listquery.Compile(
			b,
			fmap.Column,
			filter,
		), listquery.CompileOrder(
			b,
			fmap.Column,
			fmap.Join,
			fmap.Ordinal,
			// A derived sort (aggregate/interval) is store-provided (Reader.ResolveDerived), not
			// declared in the allow-list Map, so this Map-driven convenience path carries none.
			nil,
			sort,
			tiebreaker,
		)
}

// Kind identifies a query-compiler backend — the target query language a validated list
// query compiles into.
type Kind int

const (
	// KindEnt compiles to Ent dialect/sql predicates (Postgres). The default and only
	// backend today.
	KindEnt Kind = iota
	// KindMongo is reserved for a MongoDB backend (not implemented).
	KindMongo
	// KindElastic is reserved for an Elasticsearch backend (not implemented).
	KindElastic
	// KindGORM is reserved for a GORM backend (not implemented).
	KindGORM
)

// String returns the config token for the kind.
func (k Kind) String() string {
	switch k {
	case KindEnt:
		return "ent"
	case KindMongo:
		return "mongo"
	case KindElastic:
		return "elastic"
	case KindGORM:
		return "gorm"
	default:
		return "unknown"
	}
}

// ParseKind maps a config token (repos.compiler.kind) to a Kind, erroring on an unknown
// token.
func ParseKind(s string) (Kind, error) {
	switch s {
	case "ent":
		return KindEnt, nil
	case "mongo":
		return KindMongo, nil
	case "elastic":
		return KindElastic, nil
	case "gorm":
		return KindGORM, nil
	default:
		return 0, apperr.InvalidInput("unknown query-compiler kind: " + s)
	}
}

// New returns the SQL-family (Ent) query-compiler backend for kind — the composition-root
// selection point a list store calls to obtain its injected backend. Only KindEnt is
// implemented; a reserved kind returns a coded error, since a non-SQL backend has a
// different node type and its own selector.
func New(
	kind Kind,
) (interfaces.QueryBackend[entbackend.EntPredicate, entbackend.EntPredicate], error) {
	switch kind {
	case KindEnt:
		return entbackend.New(), nil
	case KindMongo, KindElastic, KindGORM:
		return nil, apperr.InvalidInput(
			"query-compiler kind " + kind.String() + " is not implemented",
		)
	default:
		return nil, apperr.InvalidInput(
			"query-compiler kind " + kind.String() + " is not implemented",
		)
	}
}
