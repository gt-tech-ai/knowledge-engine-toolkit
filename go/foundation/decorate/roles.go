package decorate

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// The role decorators run each method of one core role interface through a Subject, so a
// decorated CRUD shape (full CRUD, read-only, list-less, insert-only) is an embedding of
// the roles it honors and every method is decorated by the same code.

// Reader decorates interfaces.Reader: Get runs as the "get" operation.
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
	return r.op.Exec(ctx, "get", func(ctx context.Context) (*T, error) {
		return r.inner.Get(ctx, id)
	})
}

// Lister decorates interfaces.Lister: List runs as the "list" operation.
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
	op Subject
}

// NewCreator decorates inner's Create through op, validating each input with validate
// when it is non-nil.
func NewCreator[T, C any](
	op Subject,
	inner interfaces.Creator[T, C],
	validate func(C) error,
) Creator[T, C] {
	return Creator[T, C]{op: op, inner: inner, validate: validate}
}

// Create persists a new entity built from in.
func (w Creator[T, C]) Create(ctx context.Context, in C) (*T, error) {
	return w.op.Exec(ctx, "create", func(ctx context.Context) (*T, error) {
		if err := check(w.validate, in); err != nil {
			return nil, err
		}
		return w.inner.Create(ctx, in)
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
	op Subject
}

// NewUpdater decorates inner's Update through op, validating each input with validate
// when it is non-nil.
func NewUpdater[T any, ID comparable, U any](
	op Subject,
	inner interfaces.Updater[T, ID, U],
	validate func(U) error,
) Updater[T, ID, U] {
	return Updater[T, ID, U]{op: op, inner: inner, validate: validate}
}

// Update modifies the entity identified by id with in.
func (w Updater[T, ID, U]) Update(ctx context.Context, id ID, in U) (*T, error) {
	return w.op.Exec(ctx, "update", func(ctx context.Context) (*T, error) {
		if err := check(w.validate, in); err != nil {
			return nil, err
		}
		return w.inner.Update(ctx, id, in)
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
func NewWriter[T any, ID comparable](
	create, update Subject,
	inner interfaces.Writer[T, ID],
	validate func(*T) error,
) Writer[T, ID] {
	return Writer[T, ID]{
		Creator: NewCreator[T, *T](create, inner, validate),
		Updater: NewUpdater[T, ID, *T](update, inner, validate),
	}
}

// Suggester decorates interfaces.Suggester: SuggestValues runs as the "suggest_values"
// operation.
type Suggester[P any] struct {
	// inner is the decorated role.
	inner interfaces.Suggester[P]

	// op runs the operation.
	op Subject
}

// NewSuggester decorates inner's SuggestValues through op.
func NewSuggester[P any](op Subject, inner interfaces.Suggester[P]) Suggester[P] {
	return Suggester[P]{op: op, inner: inner}
}

// SuggestValues returns the distinct values params selects.
func (s Suggester[P]) SuggestValues(
	ctx context.Context,
	params P,
) (*types.Page[string], error) {
	return s.op.Exec(
		ctx,
		"suggest_values",
		func(ctx context.Context) (*types.Page[string], error) {
			return s.inner.SuggestValues(ctx, params)
		},
	)
}

// Inserter decorates interfaces.Inserter: Insert runs as the "insert" operation.
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
	_, err := i.op.Exec(ctx, "insert", func(ctx context.Context) (struct{}, error) {
		return struct{}{}, i.inner.Insert(ctx, entity)
	})
	return err
}

// Deleter decorates interfaces.Deleter: Delete runs as the "delete" operation.
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
	_, err := d.op.Exec(ctx, "delete", func(ctx context.Context) (struct{}, error) {
		return struct{}{}, d.inner.Delete(ctx, id)
	})
	return err
}

// Exister decorates interfaces.Exister: Exists runs as the "exists" operation.
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
	return e.op.Exec(ctx, "exists", func(ctx context.Context) (bool, error) {
		return e.inner.Exists(ctx, id)
	})
}

// Resource decorates interfaces.Resource, composed of the role decorators.
type Resource[T, P any, ID comparable, C, U any] struct {
	// Reader contributes the decorated Get.
	Reader[T, ID]
	// Lister contributes the decorated List.
	Lister[T, P]
	// Creator contributes the decorated Create.
	Creator[T, C]
	// Updater contributes the decorated Update.
	Updater[T, ID, U]
	// Deleter contributes the decorated Delete.
	Deleter[ID]
	// Exister contributes the decorated Exists.
	Exister[ID]
}

// ResourceOps are the Subjects a decorated Resource runs through: Create's, apart since
// a create is not idempotent and so is never retried, and every other role's.
type ResourceOps struct {
	// Create runs Create.
	Create Subject

	// Rest runs every role but Create.
	Rest Subject
}

// NewResource decorates inner's roles through ops, validating a Create input with
// validateCreate and an Update input with validateUpdate when each is non-nil.
func NewResource[T, P any, ID comparable, C, U any](
	ops ResourceOps,
	inner interfaces.Resource[T, P, ID, C, U],
	validateCreate func(C) error,
	validateUpdate func(U) error,
) Resource[T, P, ID, C, U] {
	return Resource[T, P, ID, C, U]{
		Reader:  NewReader(ops.Rest, inner),
		Lister:  NewLister(ops.Rest, inner),
		Creator: NewCreator(ops.Create, inner, validateCreate),
		Updater: NewUpdater(ops.Rest, inner, validateUpdate),
		Deleter: NewDeleter(ops.Rest, inner),
		Exister: NewExister(ops.Rest, inner),
	}
}

// NewOutbox returns inner with Claim run through claim and every other operation
// through rest.
func NewOutbox(claim, rest Subject, inner interfaces.OutboxStore) Outbox {
	return Outbox{inner: inner, claim: claim, op: rest}
}

var _ interfaces.OutboxStore = Outbox{}

// Outbox runs each outbox-store operation through a tier: Claim on its own Subject
// (a repository's runs it without retry, since a claim leases rows and counts an
// attempt), every other operation on the rest.
type Outbox struct {
	// inner is the wrapped store.
	inner interfaces.OutboxStore

	// claim runs Claim.
	claim Subject

	// op runs every other operation.
	op Subject
}

// Claim leases up to limit due rows of lane; see interfaces.OutboxStore.
func (r Outbox) Claim(
	ctx context.Context,
	lane string,
	limit int,
) ([]types.OutboxRecord, error) {
	return r.claim.Exec(
		ctx,
		"claim",
		func(ctx context.Context) ([]types.OutboxRecord, error) {
			return r.inner.Claim(ctx, lane, limit)
		},
	)
}

// MarkSent records that row id was delivered.
func (r Outbox) MarkSent(ctx context.Context, id uuid.UUID) error {
	return r.do(ctx, "mark_sent", func(ctx context.Context) error {
		return r.inner.MarkSent(ctx, id)
	})
}

// Retry makes row id claimable again at next, recording lastErr.
func (r Outbox) Retry(
	ctx context.Context,
	id uuid.UUID,
	next time.Time,
	lastErr string,
) error {
	return r.do(ctx, "retry", func(ctx context.Context) error {
		return r.inner.Retry(ctx, id, next, lastErr)
	})
}

// Park takes row id out of rotation, recording lastErr.
func (r Outbox) Park(ctx context.Context, id uuid.UUID, lastErr string) error {
	return r.do(ctx, "park", func(ctx context.Context) error {
		return r.inner.Park(ctx, id, lastErr)
	})
}

// Stats summarizes lane's pending and parked rows.
func (r Outbox) Stats(
	ctx context.Context,
	lane string,
) (types.OutboxStats, error) {
	return r.op.Exec(ctx, "stats", func(ctx context.Context) (types.OutboxStats, error) {
		return r.inner.Stats(ctx, lane)
	})
}

// DeleteSentBefore purges the rows delivered before cutoff and returns how many.
func (r Outbox) DeleteSentBefore(
	ctx context.Context,
	cutoff time.Time,
) (int, error) {
	return r.op.Exec(ctx, "delete_sent_before", func(ctx context.Context) (int, error) {
		return r.inner.DeleteSentBefore(ctx, cutoff)
	})
}

// do runs an error-only operation through the chain.
func (r Outbox) do(
	ctx context.Context,
	op string,
	fn func(context.Context) error,
) error {
	_, err := r.op.Exec(ctx, op, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, fn(ctx)
	})
	return err
}
