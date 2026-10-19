package repository

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Entity is the repository-tier entity base: a Base that also serves the entity's roles
// over N, the next tier's contract (a store). Reads and idempotent writes run through the
// full chain, so a transient failure is retried; Create runs without retry, since it is
// not idempotent; Insert and Upsert run observed only, inside the caller's transaction. A
// repository embeds it and adds its product queries through Exec, calling Next.
type Entity[T, P any, ID comparable, N any] struct {
	// Entity serves the roles.
	decorate.Entity[T, P, ID]

	// next is the store.
	next N

	// Base runs the repository's operations and supplies Name, Exec and ExecOnce.
	Base
}

// Next returns the store the repository is built over, as its contract declares it.
func (e Entity[T, P, ID, N]) Next() N { return e.next }

// NewEntity builds the Entity of the repository named name over next, whose contract is
// R (an interface), serving the roles R has from next's methods: decorated as NewBase
// decorates. A non-nil cache puts the read-through entity cache beneath the roles. A role
// R names that next lacks is CodeInternal.
func NewEntity[R, T, P any, ID comparable, N any](
	decs decorate.Decorations,
	name string,
	next N,
	cache *Cache[T, ID],
) (Entity[T, P, ID, N], error) {
	b, err := NewBase[R](decs, name)
	if err != nil {
		return Entity[T, P, ID, N]{}, err
	}
	roles := decorate.RolesOf[T, P, ID](next)
	if cache != nil {
		roles = cached(roles, *cache, name)
	}
	run := func(
		ctx context.Context,
		op string,
		kind decorate.RoleKind,
		fn func(context.Context) error,
	) error {
		s := b.Subject
		switch kind {
		case decorate.RoleCreate:
			s = b.Once()
		case decorate.RoleAppend:
			s = b.Observed()
		case decorate.RoleRead, decorate.RoleWrite:
		}
		_, err := s.Exec(ctx, op, func(ctx context.Context) (struct{}, error) {
			return struct{}{}, fn(ctx)
		})
		return err
	}
	e, err := decorate.NewEntity[R](name, roles, run, nil)
	if err != nil {
		return Entity[T, P, ID, N]{}, err
	}
	return Entity[T, P, ID, N]{Base: b, Entity: e, next: next}, nil
}
