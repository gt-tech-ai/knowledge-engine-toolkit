package service

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Entity is the service-tier entity base: a Base that also serves the entity's roles over
// N, the next tier's contract (a repository), checking each entity Create, Update and
// Upsert write with the service's validator. A service embeds it and adds its domain
// operations through Exec, calling Next; an override of a role calls Next, never the
// role it shadows.
type Entity[T, P any, ID comparable, N any] struct {
	// Entity serves the roles.
	decorate.Entity[T, P, ID]

	// next is the repository.
	next N

	// Base runs the service's operations and supplies Name and Exec.
	Base
}

// Next returns the repository the service is built over, as its contract declares it.
func (e Entity[T, P, ID, N]) Next() N { return e.next }

// NewEntity builds the Entity of the service named name over next, whose contract is S
// (an interface), serving the roles S has from next's methods and rejecting a written
// entity validate refuses (nil accepts every entity): decorated as NewBase decorates. A
// role S names that next lacks is CodeInternal.
func NewEntity[S, T, P any, ID comparable, N any](
	decs decorate.Decorations,
	name string,
	next N,
	validate func(*T) error,
) (Entity[T, P, ID, N], error) {
	b, err := NewBase[S](decs, name)
	if err != nil {
		return Entity[T, P, ID, N]{}, err
	}
	e, err := decorate.NewEntity[S](
		name,
		decorate.RolesOf[T, P, ID](next),
		b.run,
		validate,
	)
	if err != nil {
		return Entity[T, P, ID, N]{}, err
	}
	return Entity[T, P, ID, N]{Base: b, Entity: e, next: next}, nil
}

// run runs every role call through the service's chain.
func (b Base) run(
	ctx context.Context,
	op string,
	_ decorate.RoleKind,
	fn func(context.Context) error,
) error {
	_, err := b.Exec(
		ctx,
		op,
		func(ctx context.Context) (struct{}, error) { return struct{}{}, fn(ctx) },
	)
	return err
}
