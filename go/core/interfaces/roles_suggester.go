package interfaces

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Suggester returns the distinct values of a field matching a prefix, as a page.
type Suggester[P any] interface {
	// SuggestValues returns the distinct values params selects.
	SuggestValues(ctx context.Context, params P) (*types.Page[string], error)
}

// Resource is the full keyed resource: Get, List, Create from C, Update from U, Delete
// and Exists. A tier's Resource Base implements it; Store and Repository are the
// Resource whose writes take the entity itself.
type Resource[T any, P any, ID comparable, C any, U any] interface {
	// Reader contributes Get (read by id).
	Reader[T, ID]
	// Lister contributes List (paged read).
	Lister[T, P]
	// Creator contributes Create.
	Creator[T, C]
	// Updater contributes Update.
	Updater[T, ID, U]
	// Deleter contributes Delete (remove by id).
	Deleter[ID]
	// Exister contributes Exists (existence check by id).
	Exister[ID]
}

// ReadListExistStore is the read-only persistence surface: Get + List + Exists, with no
// write role. It is the store seam for a resource whose writes are owned elsewhere (e.g.
// a read model another service writes).
type ReadListExistStore[T any, P any, ID comparable] interface {
	// Reader contributes Get (read by id).
	Reader[T, ID]
	// Lister contributes List (paged read).
	Lister[T, P]
	// Exister contributes Exists (existence check by id).
	Exister[ID]
}

// CRUDNoListStore is the list-less full-write persistence surface: Get + Create +
// Update + Delete + Exists, with no List. It is the store seam for a keyed resource with
// no listable collection (e.g. one addressed only by id or an external id).
type CRUDNoListStore[T any, ID comparable] interface {
	// Reader contributes Get (read by id).
	Reader[T, ID]
	// Writer contributes Create and Update.
	Writer[T, ID]
	// Deleter contributes Delete (remove by id).
	Deleter[ID]
	// Exister contributes Exists (existence check by id).
	Exister[ID]
}

// CRUDNoListRepository is the list-less repository surface a list-less service
// delegates to: Get + Create + Update + Delete (services expose no Exists). A list-less
// decorated repository (Reader + Writer + Deleter + Exister) satisfies it.
type CRUDNoListRepository[T any, ID comparable] interface {
	// Reader contributes Get (read by id).
	Reader[T, ID]
	// Writer contributes Create and Update.
	Writer[T, ID]
	// Deleter contributes Delete (remove by id).
	Deleter[ID]
}
