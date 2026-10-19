package decorate

import (
	"context"
	"reflect"
	"strings"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// RoleKind classifies a role call, so a tier runs it the way its kind requires: a read
// may be retried, a create may not, an idempotent write may, and an append (Insert,
// Upsert) runs once inside the caller's transaction.
type RoleKind int

const (
	// RoleRead is Get, List or Exists.
	RoleRead RoleKind = iota

	// RoleCreate is Create, which is not idempotent.
	RoleCreate

	// RoleWrite is Update, Delete or PurgeBefore, which are idempotent.
	RoleWrite

	// RoleAppend is Insert or Upsert, which run inside the caller's transaction.
	RoleAppend
)

// RoleRunner runs one role call fn as the operation op of kind. A tier's entity base
// supplies it: the store tier runs a write through Write, the repository tier skips
// retry for a create, the workflow tier opens a transaction for a write.
type RoleRunner func(
	ctx context.Context,
	op string,
	kind RoleKind,
	fn func(context.Context) error,
) error

// Roles are the role implementations an Entity serves; a nil role is one the
// component's contract does not have.
type Roles[T, P any, ID comparable] struct {
	// Get reads the entity identified by id.
	Get func(ctx context.Context, id ID) (*T, error)

	// List reads the page of entities params and page select.
	List func(ctx context.Context, params P, page types.PageRequest) (*types.Page[T], error)

	// Create persists a new entity.
	Create func(ctx context.Context, entity *T) (*T, error)

	// Update replaces the entity identified by id.
	Update func(ctx context.Context, id ID, entity *T) (*T, error)

	// Delete removes the entity identified by id.
	Delete func(ctx context.Context, id ID) error

	// Exists reports whether the entity identified by id exists.
	Exists func(ctx context.Context, id ID) (bool, error)

	// Insert appends a new entity.
	Insert func(ctx context.Context, entity *T) error

	// Upsert inserts the entity or replaces the one with its key.
	Upsert func(ctx context.Context, entity *T) error

	// PurgeBefore removes the entities older than cutoff.
	PurgeBefore func(ctx context.Context, cutoff time.Time) (int, error)
}

// RolesOf returns the roles next has, each calling next's method of that name.
func RolesOf[T, P any, ID comparable](next any) Roles[T, P, ID] {
	var r Roles[T, P, ID]
	if n, ok := next.(interfaces.Reader[T, ID]); ok {
		r.Get = n.Get
	}
	if n, ok := next.(interfaces.Lister[T, P]); ok {
		r.List = n.List
	}
	if n, ok := next.(interfaces.Creator[T, *T]); ok {
		r.Create = n.Create
	}
	if n, ok := next.(interfaces.Updater[T, ID, *T]); ok {
		r.Update = n.Update
	}
	if n, ok := next.(interfaces.Deleter[ID]); ok {
		r.Delete = n.Delete
	}
	if n, ok := next.(interfaces.Exister[ID]); ok {
		r.Exists = n.Exists
	}
	if n, ok := next.(interfaces.Inserter[T]); ok {
		r.Insert = n.Insert
	}
	if n, ok := next.(interfaces.Upserter[T]); ok {
		r.Upsert = n.Upsert
	}
	if n, ok := next.(interfaces.Purger); ok {
		r.PurgeBefore = n.PurgeBefore
	}
	return r
}

// missing returns the roles the contract C names that r lacks.
func (r Roles[T, P, ID]) missing[C any]() []string {
	has := map[string]bool{
		"Get": r.Get != nil, "List": r.List != nil, "Create": r.Create != nil,
		"Update": r.Update != nil, "Delete": r.Delete != nil, "Exists": r.Exists != nil,
		"Insert": r.Insert != nil, "Upsert": r.Upsert != nil,
		"PurgeBefore": r.PurgeBefore != nil,
	}
	t := reflect.TypeFor[C]()
	var out []string
	for i := range t.NumMethod() {
		name := t.Method(i).Name
		if present, isRole := has[name]; isRole && !present {
			out = append(out, name)
		}
	}
	return out
}

// Entity serves an entity's roles, each through run under its operation name. It is the
// core every tier's entity base embeds: the tier supplies run and the roles.
type Entity[T, P any, ID comparable] struct {
	// roles are the implementations served.
	roles Roles[T, P, ID]

	// validate, when non-nil, rejects an invalid entity before Create, Update or Upsert.
	validate func(*T) error

	// run runs each role call the tier's way.
	run RoleRunner
}

// NewEntity returns the Entity of the component named name whose contract is C, serving
// roles through run and checking each written entity with validate when it is non-nil.
// A role C names that roles lacks is CodeInternal.
func NewEntity[C, T, P any, ID comparable](
	name string,
	roles Roles[T, P, ID],
	run RoleRunner,
	validate func(*T) error,
) (Entity[T, P, ID], error) {
	if missing := roles.missing[C](); len(missing) > 0 {
		return Entity[T, P, ID]{}, apperr.New(
			apperr.CodeInternal,
			name+": no implementation of "+strings.Join(missing, ", "),
		)
	}
	return Entity[T, P, ID]{roles: roles, run: run, validate: validate}, nil
}

// call runs fn as op through run, returning fn's result. A role that is not present is
// one the contract does not have, refused as CodeInternal; a nil run (the zero Entity)
// runs fn undecorated.
func call[R any](
	ctx context.Context,
	run RoleRunner,
	op string,
	kind RoleKind,
	present bool,
	fn func(context.Context) (R, error),
) (R, error) {
	var out R
	if !present {
		return out, apperr.New(apperr.CodeInternal, op+" is not a role of this component")
	}
	if run == nil {
		return fn(ctx)
	}
	err := run(ctx, op, kind, func(ctx context.Context) error {
		var err error
		out, err = fn(ctx)
		return err
	})
	return out, err
}

// check validates v when a validator is set.
func (e Entity[T, P, ID]) check(v *T) error {
	if e.validate == nil {
		return nil
	}
	return e.validate(v)
}

// Get returns the entity identified by id.
func (e Entity[T, P, ID]) Get(ctx context.Context, id ID) (*T, error) {
	return call(ctx, e.run, "get", RoleRead, e.roles.Get != nil,
		func(ctx context.Context) (*T, error) { return e.roles.Get(ctx, id) })
}

// List returns the page of entities params and page select.
func (e Entity[T, P, ID]) List(
	ctx context.Context,
	params P,
	page types.PageRequest,
) (*types.Page[T], error) {
	return call(ctx, e.run, "list", RoleRead, e.roles.List != nil,
		func(ctx context.Context) (*types.Page[T], error) {
			return e.roles.List(ctx, params, page)
		})
}

// Create persists entity, after the validator accepts it.
func (e Entity[T, P, ID]) Create(ctx context.Context, entity *T) (*T, error) {
	return call(ctx, e.run, "create", RoleCreate, e.roles.Create != nil,
		func(ctx context.Context) (*T, error) {
			if err := e.check(entity); err != nil {
				return nil, err
			}
			return e.roles.Create(ctx, entity)
		})
}

// Update replaces the entity identified by id, after the validator accepts entity.
func (e Entity[T, P, ID]) Update(ctx context.Context, id ID, entity *T) (*T, error) {
	return call(ctx, e.run, "update", RoleWrite, e.roles.Update != nil,
		func(ctx context.Context) (*T, error) {
			if err := e.check(entity); err != nil {
				return nil, err
			}
			return e.roles.Update(ctx, id, entity)
		})
}

// Delete removes the entity identified by id.
func (e Entity[T, P, ID]) Delete(ctx context.Context, id ID) error {
	_, err := call(
		ctx,
		e.run,
		"delete",
		RoleWrite,
		e.roles.Delete != nil,
		func(ctx context.Context) (struct{}, error) {
			return struct{}{}, e.roles.Delete(ctx, id)
		},
	)
	return err
}

// Exists reports whether the entity identified by id exists.
func (e Entity[T, P, ID]) Exists(ctx context.Context, id ID) (bool, error) {
	return call(ctx, e.run, "exists", RoleRead, e.roles.Exists != nil,
		func(ctx context.Context) (bool, error) { return e.roles.Exists(ctx, id) })
}

// Insert appends entity.
func (e Entity[T, P, ID]) Insert(ctx context.Context, entity *T) error {
	_, err := call(ctx, e.run, "insert", RoleAppend, e.roles.Insert != nil,
		func(ctx context.Context) (struct{}, error) {
			return struct{}{}, e.roles.Insert(ctx, entity)
		})
	return err
}

// Upsert inserts entity or replaces the one with its key, after the validator accepts it.
func (e Entity[T, P, ID]) Upsert(ctx context.Context, entity *T) error {
	_, err := call(ctx, e.run, "upsert", RoleAppend, e.roles.Upsert != nil,
		func(ctx context.Context) (struct{}, error) {
			if err := e.check(entity); err != nil {
				return struct{}{}, err
			}
			return struct{}{}, e.roles.Upsert(ctx, entity)
		})
	return err
}

// PurgeBefore removes the entities older than cutoff and returns how many it removed.
func (e Entity[T, P, ID]) PurgeBefore(
	ctx context.Context,
	cutoff time.Time,
) (int, error) {
	return call(
		ctx,
		e.run,
		"purge_before",
		RoleWrite,
		e.roles.PurgeBefore != nil,
		func(ctx context.Context) (int, error) { return e.roles.PurgeBefore(ctx, cutoff) },
	)
}
