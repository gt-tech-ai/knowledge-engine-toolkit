// Package role holds the role decorators: each core role interface run through a
// decorate.Subject, so a decorated CRUD shape is an embedding of the roles it honors.
package role

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// The role decorators run each method of one core role interface through a Subject, so a
// decorated CRUD shape (full CRUD, read-only, list-less, insert-only) is an embedding of
// the roles it honors and every method is decorated by the same code.

// Reader decorates interfaces.Reader: Get runs as the "get" operation.
type Reader[T any, ID comparable] struct {
	// inner is the decorated role.
	inner interfaces.Reader[T, ID]

	// op runs the operation.
	op decorate.Subject
}

// NewReader decorates inner's Get through op.
// op must not be nil.
func NewReader[T any, ID comparable](
	op *decorate.Subject,
	inner interfaces.Reader[T, ID],
) Reader[T, ID] {
	return Reader[T, ID]{op: *op, inner: inner}
}

// Get reads one entity by id.
func (r Reader[T, ID]) Get(ctx context.Context, id ID) (*T, error) {
	return r.op.Exec(ctx, "get", func(ctx context.Context) (*T, error) {
		return r.inner.Get(ctx, id)
	})
}

// Lister decorates interfaces.Lister: List runs as the "list" operation.
type Lister[T any, P any] struct {
	// inner is the decorated role.
	inner interfaces.Lister[T, P]

	// op runs the operation.
	op decorate.Subject
}

// NewLister decorates inner's List through op.
// op must not be nil.
func NewLister[T, P any](
	op *decorate.Subject,
	inner interfaces.Lister[T, P],
) Lister[T, P] {
	return Lister[T, P]{op: *op, inner: inner}
}

// List reads one page of entities matching params.
func (l Lister[T, P]) List(
	ctx context.Context,
	params P,
	page types.PageRequest,
) (*types.Page[T], error) {
	return l.op.Exec(ctx, "list", func(ctx context.Context) (*types.Page[T], error) {
		return l.inner.List(ctx, params, page)
	})
}

// Creator decorates interfaces.Creator: Create runs as the "create" operation, and a
// non-nil validate rejects an invalid input before it reaches inner.
type Creator[T, C any] struct {
	// inner is the decorated role.
	inner interfaces.Creator[T, C]

	// validate, when non-nil, checks an input before it is written.
	validate func(C) error

	// op runs the operation.
	op decorate.Subject
}

// NewCreator decorates inner's Create through op, validating each input with validate
// when it is non-nil.
// op must not be nil.
func NewCreator[T, C any](
	op *decorate.Subject,
	inner interfaces.Creator[T, C],
	validate func(C) error,
) Creator[T, C] {
	return Creator[T, C]{op: *op, inner: inner, validate: validate}
}

// Create persists a new entity built from in.
func (w Creator[T, C]) Create(ctx context.Context, value C) (*T, error) {
	return w.op.Exec(ctx, "create", func(ctx context.Context) (*T, error) {
		err := check(w.validate, value)
		if err != nil {
			return nil, err
		}

		return w.inner.Create(ctx, value)
	})
}

// Updater decorates interfaces.Updater: Update runs as the "update" operation, and a
// non-nil validate rejects an invalid input before it reaches inner.
type Updater[T any, ID comparable, U any] struct {
	// inner is the decorated role.
	inner interfaces.Updater[T, ID, U]

	// validate, when non-nil, checks an input before it is written.
	validate func(U) error

	// op runs the operation.
	op decorate.Subject
}

// NewUpdater decorates inner's Update through op, validating each input with validate
// when it is non-nil.
// op must not be nil.
func NewUpdater[T any, ID comparable, U any](
	op *decorate.Subject,
	inner interfaces.Updater[T, ID, U],
	validate func(U) error,
) Updater[T, ID, U] {
	return Updater[T, ID, U]{op: *op, inner: inner, validate: validate}
}

// Update modifies the entity identified by id with in.
func (w Updater[T, ID, U]) Update(
	ctx context.Context,
	identifier ID,
	value U,
) (*T, error) {
	return w.op.Exec(ctx, "update", func(ctx context.Context) (*T, error) {
		err := check(w.validate, value)
		if err != nil {
			return nil, err
		}

		return w.inner.Update(ctx, identifier, value)
	})
}

// check runs validate on in when one is set.
func check[V any](validate func(V) error, in V) error {
	if validate == nil {
		return nil
	}

	return validate(in)
}

// Writer decorates interfaces.Writer: Create runs through its own Subject (one without
// retry, since a create is not idempotent) and Update through the other; a non-nil
// validate rejects an invalid entity before either reaches inner.
type Writer[T any, ID comparable] struct {
	// Creator contributes the decorated Create.
	Creator[T, *T]
	// Updater contributes the decorated Update.
	Updater[T, ID, *T]
}

// NewWriter decorates inner's Create through create and Update through update,
// validating each entity with validate when it is non-nil.
// create must not be nil.
// update must not be nil.
func NewWriter[T any, ID comparable](
	create, updateRef *decorate.Subject,
	inner interfaces.Writer[T, ID],
	validate func(*T) error,
) Writer[T, ID] {
	update := *updateRef

	return Writer[T, ID]{
		Creator: NewCreator[T, *T](create, inner, validate),
		Updater: NewUpdater[T, ID, *T](&update, inner, validate),
	}
}

// NewSuggester decorates inner's SuggestValues through op.
// op must not be nil.
func NewSuggester[P any](
	op *decorate.Subject,
	inner interfaces.Suggester[P],
) Suggester[P] {
	return Suggester[P]{op: *op, inner: inner}
}

// NewInserter decorates inner's Insert through op.
// op must not be nil.
func NewInserter[T any](op *decorate.Subject, inner interfaces.Inserter[T]) Inserter[T] {
	return Inserter[T]{op: *op, inner: inner}
}

// NewDeleter decorates inner's Delete through op.
// op must not be nil.
func NewDeleter[ID comparable](
	op *decorate.Subject,
	inner interfaces.Deleter[ID],
) Deleter[ID] {
	return Deleter[ID]{op: *op, inner: inner}
}

// NewExister decorates inner's Exists through op.
// op must not be nil.
func NewExister[ID comparable](
	op *decorate.Subject,
	inner interfaces.Exister[ID],
) Exister[ID] {
	return Exister[ID]{op: *op, inner: inner}
}

// NewResource decorates inner's roles through ops, validating a Create input with
// validateCreate and an Update input with validateUpdate when each is non-nil.
// ops must not be nil.
func NewResource[T, P any, ID comparable, C, U any](
	ops *ResourceOps,
	inner interfaces.Resource[T, P, ID, C, U],
	validateCreate func(C) error,
	validateUpdate func(U) error,
) Resource[T, P, ID, C, U] {
	return Resource[T, P, ID, C, U]{
		Reader:  NewReader(&ops.Rest, inner),
		Lister:  NewLister(&ops.Rest, inner),
		Creator: NewCreator(&ops.Create, inner, validateCreate),
		Updater: NewUpdater(&ops.Rest, inner, validateUpdate),
		Deleter: NewDeleter(&ops.Rest, inner),
		Exister: NewExister(&ops.Rest, inner),
	}
}
