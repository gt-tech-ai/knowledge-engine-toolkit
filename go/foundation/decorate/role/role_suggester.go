package role

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Suggester decorates interfaces.Suggester: SuggestValues runs as the "suggest_values"
// operation.
type Suggester[P any] struct {
	// inner is the decorated role.
	inner interfaces.Suggester[P]

	// op runs the operation.
	op decorate.Subject
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
	op decorate.Subject
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
	op decorate.Subject
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
	op decorate.Subject
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
