package decorate

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// The role decorators run each method of one core role interface through a Subject, so a
// decorated CRUD shape (full CRUD, read-only, list-less, insert-only) is an embedding of
// the roles it honors and every method is decorated by the same code.

// Reader decorates interfaces.Reader: Get runs as the "Get" operation.
type Reader[T any, ID comparable] struct {
	// inner is the decorated role.
	inner interfaces.Reader[T, ID]

	// op runs the operation.
	op Subject
}

// NewReader decorates inner's Get through op.
func NewReader[T any, ID comparable](
	op Subject,
	inner interfaces.Reader[T, ID],
) Reader[T, ID] {
	return Reader[T, ID]{op: op, inner: inner}
}

// Get reads one entity by id.
func (r Reader[T, ID]) Get(ctx context.Context, id ID) (*T, error) {
	return r.op.Exec(ctx, "Get", func(ctx context.Context) (*T, error) {
		return r.inner.Get(ctx, id)
	})
}

// Lister decorates interfaces.Lister: List runs as the "List" operation.
type Lister[T any, P any] struct {
	// inner is the decorated role.
	inner interfaces.Lister[T, P]

	// op runs the operation.
	op Subject
}

// NewLister decorates inner's List through op.
func NewLister[T, P any](op Subject, inner interfaces.Lister[T, P]) Lister[T, P] {
	return Lister[T, P]{op: op, inner: inner}
}

// List reads one page of entities matching params.
func (l Lister[T, P]) List(
	ctx context.Context,
	params P,
	page types.PageRequest,
) (*types.Page[T], error) {
	return l.op.Exec(ctx, "List", func(ctx context.Context) (*types.Page[T], error) {
		return l.inner.List(ctx, params, page)
	})
}

// Writer decorates interfaces.Writer: Create runs through its own Subject (one without
// retry, since a create is not idempotent) and Update through the other; a non-nil
// validate rejects an invalid entity before either reaches inner.
type Writer[T any, ID comparable] struct {
	// inner is the decorated role.
	inner interfaces.Writer[T, ID]

	// validate, when non-nil, checks an entity before it is written.
	validate func(*T) error

	// create runs Create.
	create Subject

	// update runs Update.
	update Subject
}

// NewWriter decorates inner's Create through create and Update through update,
// validating each entity with validate when it is non-nil.
func NewWriter[T any, ID comparable](
	create, update Subject,
	inner interfaces.Writer[T, ID],
	validate func(*T) error,
) Writer[T, ID] {
	return Writer[T, ID]{create: create, update: update, inner: inner, validate: validate}
}

// Create persists a new entity.
func (w Writer[T, ID]) Create(ctx context.Context, entity *T) (*T, error) {
	return w.create.Exec(ctx, "Create", func(ctx context.Context) (*T, error) {
		if err := w.check(entity); err != nil {
			return nil, err
		}
		return w.inner.Create(ctx, entity)
	})
}

// Update modifies the entity identified by id.
func (w Writer[T, ID]) Update(ctx context.Context, id ID, entity *T) (*T, error) {
	return w.update.Exec(ctx, "Update", func(ctx context.Context) (*T, error) {
		if err := w.check(entity); err != nil {
			return nil, err
		}
		return w.inner.Update(ctx, id, entity)
	})
}

// check runs validate on entity when one is set.
func (w Writer[T, ID]) check(entity *T) error {
	if w.validate == nil {
		return nil
	}
	return w.validate(entity)
}

// Inserter decorates interfaces.Inserter: Insert runs as the "Insert" operation.
type Inserter[T any] struct {
	// inner is the decorated role.
	inner interfaces.Inserter[T]

	// op runs the operation.
	op Subject
}

// NewInserter decorates inner's Insert through op.
func NewInserter[T any](op Subject, inner interfaces.Inserter[T]) Inserter[T] {
	return Inserter[T]{op: op, inner: inner}
}

// Insert appends a new entity.
func (i Inserter[T]) Insert(ctx context.Context, entity *T) error {
	_, err := i.op.Exec(ctx, "Insert", func(ctx context.Context) (struct{}, error) {
		return struct{}{}, i.inner.Insert(ctx, entity)
	})
	return err
}

// Deleter decorates interfaces.Deleter: Delete runs as the "Delete" operation.
type Deleter[ID comparable] struct {
	// inner is the decorated role.
	inner interfaces.Deleter[ID]

	// op runs the operation.
	op Subject
}

// NewDeleter decorates inner's Delete through op.
func NewDeleter[ID comparable](op Subject, inner interfaces.Deleter[ID]) Deleter[ID] {
	return Deleter[ID]{op: op, inner: inner}
}

// Delete removes the entity identified by id.
func (d Deleter[ID]) Delete(ctx context.Context, id ID) error {
	_, err := d.op.Exec(ctx, "Delete", func(ctx context.Context) (struct{}, error) {
		return struct{}{}, d.inner.Delete(ctx, id)
	})
	return err
}

// Exister decorates interfaces.Exister: Exists runs as the "Exists" operation.
type Exister[ID comparable] struct {
	// inner is the decorated role.
	inner interfaces.Exister[ID]

	// op runs the operation.
	op Subject
}

// NewExister decorates inner's Exists through op.
func NewExister[ID comparable](op Subject, inner interfaces.Exister[ID]) Exister[ID] {
	return Exister[ID]{op: op, inner: inner}
}

// Exists reports whether the entity identified by id exists.
func (e Exister[ID]) Exists(ctx context.Context, id ID) (bool, error) {
	return e.op.Exec(ctx, "Exists", func(ctx context.Context) (bool, error) {
		return e.inner.Exists(ctx, id)
	})
}
