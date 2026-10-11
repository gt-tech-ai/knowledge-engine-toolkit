package interfaces

import (
	"context"
)

// Service is the generic business logic layer interface, composed from the role
// interfaces. It carries the same Get/List/Create/Update/Delete
// surface as before (no Exists — existence checks are a store/repository concern),
// now assembled from Reader/Lister/Writer/Deleter so a service can depend on only
// the roles it uses.
// T = entity type, P = query/filter params, ID = identifier type.
//
// Services wrap repositories with business rules, authorization, validation,
// event publishing, and caching.
type Service[T any, P any, ID comparable] interface {
	// Reader provides Get (retrieve by id).
	Reader[T, ID]
	// Lister provides List (paginated query by params).
	Lister[T, P]
	// Writer provides Create and Update.
	Writer[T, ID]
	// Deleter provides Delete (remove by id).
	Deleter[ID]
}

// DecoratedService is the fully-decorated surface of a service (the base Service
// methods wrapped by the decorator stack). It is the type the service decorator
// builder returns and that concrete services embed. Custom (non-CRUD) methods no
// longer decorate through a method here — they run through the type-agnostic
// decorate.OpMiddleware chain via decorate.Exec[R], which lets them return
// any type without the former (*T,error) shoehorn.
type DecoratedService[T any, P any, ID comparable] interface {
	// Service is the base CRUD surface the decorator stack wraps.
	Service[T, P, ID]
}

// ServiceHooks provides lifecycle hooks for service operations.
// Implementations can override specific hooks while using no-op defaults for others.
type ServiceHooks[T any] interface {
	// BeforeCreate is called before entity creation. Can modify the entity or return an
	// error.
	BeforeCreate(ctx context.Context, entity *T) error

	// AfterCreate is called after successful entity creation.
	AfterCreate(ctx context.Context, entity *T) error

	// BeforeUpdate is called before entity update. Can modify the entity or return an error.
	BeforeUpdate(ctx context.Context, entity *T) error

	// AfterUpdate is called after successful entity update.
	AfterUpdate(ctx context.Context, entity *T) error

	// BeforeDelete is called before entity deletion.
	BeforeDelete(ctx context.Context, id any) error

	// AfterDelete is called after successful entity deletion.
	AfterDelete(ctx context.Context, id any) error
}
