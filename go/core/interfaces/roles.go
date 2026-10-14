package interfaces

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// The role interfaces below decompose the fat data-access contract into small, composable
// capabilities (ARCHITECTURE.md#interface-composition). Store/Repository/Service are
// defined as compositions of these roles, so a full-CRUD resource embeds the whole set
// while a read-only or append-only resource embeds only the roles it honors — instead of
// implementing (and panicking) the operations it does not support.
//
// T = entity type, P = query/filter params, ID = identifier type. Each role's
// method signatures byte-match the corresponding Store/Repository/Service methods,
// so composing them yields the identical method set (a behavior-preserving refactor).

// Reader retrieves a single entity by its identifier.
type Reader[T any, ID comparable] interface {
	// Get retrieves an entity by ID.
	Get(ctx context.Context, id ID) (*T, error)
}

// Lister retrieves a paginated set of entities matching filter parameters.
type Lister[T any, P any] interface {
	// List retrieves entities matching the given parameters with pagination.
	List(ctx context.Context, params P, page types.PageRequest) (*types.Page[T], error)
}

// Writer creates and updates entities.
type Writer[T any, ID comparable] interface {
	// Create persists a new entity and returns it with generated fields populated.
	Create(ctx context.Context, entity *T) (*T, error)

	// Update modifies an existing entity identified by id.
	Update(ctx context.Context, id ID, entity *T) (*T, error)
}

// Updater is the standalone update-only write role: a single Update that modifies an
// existing entity. It is the mirror of Inserter for the read-write side — a resource
// whose creation happens through a bespoke path (not the generic Create) but whose
// updates are the plain keyed mutation — e.g. a membership created only from the identity
// provider's claims on first sign-in, never through the generic Create, but updated by
// id. Like Inserter, it is NOT part of the Store/Repository/Service composition (those
// embed Writer, which carries both Create and Update); it is the role a
// create-less-but-updatable resource embeds in place of Writer.
type Updater[T any, ID comparable] interface {
	// Update modifies an existing entity identified by id.
	Update(ctx context.Context, id ID, entity *T) (*T, error)
}

// Inserter is the append-only write role: a single Insert that persists a new
// entity and reports only success/failure. Unlike Writer (Create returns the
// stored entity + Update mutates), an append-only store — an outbox relay, an
// audit log — only ever adds immutable rows, so it embeds just this. It is NOT
// part of the Store/Repository/Service composition (those use Writer.Create);
// it is the standalone role an append-only resource embeds in place of Writer.
type Inserter[T any] interface {
	// Insert persists a new entity; append-only, so it returns only an error.
	Insert(ctx context.Context, entity *T) error
}

// Deleter removes an entity by its identifier.
type Deleter[ID comparable] interface {
	// Delete removes an entity by ID.
	Delete(ctx context.Context, id ID) error
}

// Exister reports whether an entity with the given identifier exists.
type Exister[ID comparable] interface {
	// Exists checks if an entity with the given ID exists.
	Exists(ctx context.Context, id ID) (bool, error)
}

// The compositions below are the role sets a narrowed (role-scoped) resource honors;
// the role-scoped decorated repositories and services wrap them.

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

// InsertOnlyStore is the append-only, tx-aware persistence seam: a store whose Insert
// honors a transaction on the context.
type InsertOnlyStore[T any] interface {
	// Inserter contributes Insert (tx-aware append-only write).
	Inserter[T]
}
