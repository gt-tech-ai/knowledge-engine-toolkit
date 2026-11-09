package store

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Funcs are an entity store's statements, each run on the client its context's
// statement runs on (the transaction's, else the store's own). A nil statement is a role
// the store's contract does not have.
type Funcs[C, T, P any, ID comparable] struct {
	// Get reads the entity identified by id.
	Get func(ctx context.Context, c C, id ID) (*T, error)

	// List reads the page of entities params and page select.
	List func(
		ctx context.Context,
		c C,
		params P,
		page types.PageRequest,
	) (*types.Page[T], error)

	// Create writes a new entity.
	Create func(ctx context.Context, c C, entity *T) (*T, error)

	// Update writes entity to the entity identified by id.
	Update func(ctx context.Context, c C, id ID, entity *T) (*T, error)

	// Delete removes the entity identified by id.
	Delete func(ctx context.Context, c C, id ID) error

	// Exists reports whether the entity identified by id exists.
	Exists func(ctx context.Context, c C, id ID) (bool, error)

	// Insert appends a new entity.
	Insert func(ctx context.Context, c C, entity *T) error

	// Upsert writes entity, inserting it or replacing the one with its key.
	Upsert func(ctx context.Context, c C, entity *T) error

	// PurgeBefore removes the entities older than cutoff.
	PurgeBefore func(ctx context.Context, c C, cutoff time.Time) (int, error)
}

// Entity is the store-tier entity base: a Base over a client of type C that also serves
// the entity's roles from its statements. Reads run through Exec, and writes through
// Write, so a write runs only inside a transaction. A store embeds it and adds its
// product queries through Exec and Write.
type Entity[C, T, P any, ID comparable] struct {
	// Entity serves the roles.
	decorate.Entity[T, P, ID]

	// Base runs the store's statements and supplies Name, Exec, Write and Client.
	Base[C]
}

// NewEntity builds the Entity of the store named name over raw, whose contract is S (an
// interface), serving the roles S has from f: decorated as NewBase decorates. A role S
// names with no statement in f is CodeInternal.
func NewEntity[S, C, T, P any, ID comparable](
	decs decorate.Decorations,
	name string,
	raw C,
	funcs Funcs[C, T, P, ID],
) (Entity[C, T, P, ID], error) {
	base, err := NewBase[S](decs, name, raw)
	if err != nil {
		return Entity[C, T, P, ID]{}, err
	}

	run := func(
		ctx context.Context,
		operation string,
		kind decorate.RoleKind,
		fn func(context.Context) error,
	) error {
		body := func(ctx context.Context) (struct{}, error) { return struct{}{}, fn(ctx) }
		if kind == decorate.RoleRead {
			_, opErr := base.Exec(ctx, operation, body)

			return opErr
		}

		_, opErr := base.Write(ctx, operation, body)

		return opErr
	}

	e, err := decorate.NewEntity[S](name, rolesOf(base, funcs), run, nil)
	if err != nil {
		return Entity[C, T, P, ID]{}, err
	}

	return Entity[C, T, P, ID]{Base: base, Entity: e}, nil
}

// rolesOf binds each of f's statements to the client b gives its context.
func rolesOf[C, T, P any, ID comparable](
	base Base[C],
	funcs Funcs[C, T, P, ID],
) decorate.Roles[T, P, ID] {
	var roles decorate.Roles[T, P, ID]

	bindReads(&roles, base, funcs)
	bindWrites(&roles, base, funcs)
	bindBulk(&roles, base, funcs)

	return roles
}

// bindReads binds the declared read roles over the store's client.
func bindReads[C, T, P any, ID comparable](
	roles *decorate.Roles[T, P, ID],
	base Base[C],
	funcs Funcs[C, T, P, ID],
) {
	if funcs.Get != nil {
		roles.Get = func(ctx context.Context, id ID) (*T, error) {
			return funcs.Get(ctx, base.Client(ctx), id)
		}
	}

	if funcs.List != nil {
		roles.List = func(
			ctx context.Context,
			params P,
			page types.PageRequest,
		) (*types.Page[T], error) {
			return funcs.List(ctx, base.Client(ctx), params, page)
		}
	}

	if funcs.Exists != nil {
		roles.Exists = func(ctx context.Context, id ID) (bool, error) {
			return funcs.Exists(ctx, base.Client(ctx), id)
		}
	}
}

// bindWrites binds the declared single-entity write roles over the store's client.
func bindWrites[C, T, P any, ID comparable](
	roles *decorate.Roles[T, P, ID],
	base Base[C],
	funcs Funcs[C, T, P, ID],
) {
	if funcs.Create != nil {
		roles.Create = func(ctx context.Context, v *T) (*T, error) {
			return funcs.Create(ctx, base.Client(ctx), v)
		}
	}

	if funcs.Update != nil {
		roles.Update = func(ctx context.Context, id ID, v *T) (*T, error) {
			return funcs.Update(ctx, base.Client(ctx), id, v)
		}
	}

	if funcs.Delete != nil {
		roles.Delete = func(ctx context.Context, id ID) error {
			return funcs.Delete(ctx, base.Client(ctx), id)
		}
	}
}

// bindBulk binds the declared insert, upsert and purge roles over the store's client.
func bindBulk[C, T, P any, ID comparable](
	roles *decorate.Roles[T, P, ID],
	base Base[C],
	funcs Funcs[C, T, P, ID],
) {
	if funcs.Insert != nil {
		roles.Insert = func(ctx context.Context, v *T) error {
			return funcs.Insert(ctx, base.Client(ctx), v)
		}
	}

	if funcs.Upsert != nil {
		roles.Upsert = func(ctx context.Context, v *T) error {
			return funcs.Upsert(ctx, base.Client(ctx), v)
		}
	}

	if funcs.PurgeBefore != nil {
		roles.PurgeBefore = func(ctx context.Context, cutoff time.Time) (int, error) {
			return funcs.PurgeBefore(ctx, base.Client(ctx), cutoff)
		}
	}
}
