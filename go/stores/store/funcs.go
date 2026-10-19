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
	f Funcs[C, T, P, ID],
) (Entity[C, T, P, ID], error) {
	b, err := NewBase[S](decs, name, raw)
	if err != nil {
		return Entity[C, T, P, ID]{}, err
	}
	run := func(
		ctx context.Context,
		op string,
		kind decorate.RoleKind,
		fn func(context.Context) error,
	) error {
		body := func(ctx context.Context) (struct{}, error) { return struct{}{}, fn(ctx) }
		if kind == decorate.RoleRead {
			_, err := b.Exec(ctx, op, body)
			return err
		}
		_, err := b.Write(ctx, op, body)
		return err
	}
	e, err := decorate.NewEntity[S](name, rolesOf(b, f), run, nil)
	if err != nil {
		return Entity[C, T, P, ID]{}, err
	}
	return Entity[C, T, P, ID]{Base: b, Entity: e}, nil
}

// rolesOf binds each of f's statements to the client b gives its context.
func rolesOf[C, T, P any, ID comparable](
	b Base[C],
	f Funcs[C, T, P, ID],
) decorate.Roles[T, P, ID] {
	var r decorate.Roles[T, P, ID]
	if f.Get != nil {
		r.Get = func(ctx context.Context, id ID) (*T, error) {
			return f.Get(ctx, b.Client(ctx), id)
		}
	}
	if f.List != nil {
		r.List = func(
			ctx context.Context,
			params P,
			page types.PageRequest,
		) (*types.Page[T], error) {
			return f.List(ctx, b.Client(ctx), params, page)
		}
	}
	if f.Create != nil {
		r.Create = func(ctx context.Context, v *T) (*T, error) {
			return f.Create(ctx, b.Client(ctx), v)
		}
	}
	if f.Update != nil {
		r.Update = func(ctx context.Context, id ID, v *T) (*T, error) {
			return f.Update(ctx, b.Client(ctx), id, v)
		}
	}
	if f.Delete != nil {
		r.Delete = func(ctx context.Context, id ID) error {
			return f.Delete(ctx, b.Client(ctx), id)
		}
	}
	if f.Exists != nil {
		r.Exists = func(ctx context.Context, id ID) (bool, error) {
			return f.Exists(ctx, b.Client(ctx), id)
		}
	}
	if f.Insert != nil {
		r.Insert = func(ctx context.Context, v *T) error {
			return f.Insert(ctx, b.Client(ctx), v)
		}
	}
	if f.Upsert != nil {
		r.Upsert = func(ctx context.Context, v *T) error {
			return f.Upsert(ctx, b.Client(ctx), v)
		}
	}
	if f.PurgeBefore != nil {
		r.PurgeBefore = func(ctx context.Context, cutoff time.Time) (int, error) {
			return f.PurgeBefore(ctx, b.Client(ctx), cutoff)
		}
	}
	return r
}
