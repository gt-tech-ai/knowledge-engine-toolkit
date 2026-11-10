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
// T = entity type, P = query/filter params, ID = identifier type, and C / U = the input a
// Create / Update takes (*T for an entity-shaped write, a params object for a command).
// Each entity tier's Entity base serves the roles whose signatures use only T, P and ID
// (with *T as the write input), so a component embeds it and declares by hand only its
// product operations.

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

// Creator creates an entity from in and returns it with generated fields populated.
type Creator[T any, C any] interface {
	// Create persists a new entity built from in.
	Create(ctx context.Context, in C) (*T, error)
}

// Updater modifies the entity identified by id with in and returns it. A resource
// created through a bespoke path (e.g. a membership created only from the identity
// provider's claims) but updated by id embeds it alone.
type Updater[T any, ID comparable, U any] interface {
	// Update modifies an existing entity identified by id.
	Update(ctx context.Context, id ID, in U) (*T, error)
}

// Writer creates and updates entities whose write input is the entity itself.
type Writer[T any, ID comparable] interface {
	// Creator contributes Create(ctx, *T).
	Creator[T, *T]
	// Updater contributes Update(ctx, id, *T).
	Updater[T, ID, *T]
}

// The compositions below are the role sets a narrowed (role-scoped) resource honors;
// the role-scoped decorated repositories and services wrap them.
