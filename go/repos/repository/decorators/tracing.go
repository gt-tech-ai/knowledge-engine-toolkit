package decorators

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// tracingDecorator adds distributed trace spans to repository operations.
type tracingDecorator[T any, P any, ID comparable] struct {
	// inner is the next repository in the decorator chain.
	inner interfaces.DecoratedRepository[T, P, ID]

	// tracer creates spans for each operation.
	tracer interfaces.Tracer

	// name is the repository name used as a span name prefix.
	name string
}

// Get wraps the inner Get in a span, recording the error and error status on
// the span when the operation fails.
func (d *tracingDecorator[T, P, ID]) Get(ctx context.Context, id ID) (*T, error) {
	ctx, span := d.tracer.Start(ctx, d.name+".Get")
	defer span.End()
	span.SetAttribute("db.repository", d.name)
	span.SetAttribute("db.operation", "Get")
	result, err := d.inner.Get(ctx, id)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(interfaces.SpanStatusError, err.Error())
	}
	return result, err
}

// List wraps the inner List in a span, recording the error and error status on
// the span when the operation fails.
func (d *tracingDecorator[T, P, ID]) List(
	ctx context.Context,
	params P,
	page types.PageRequest,
) (*types.Page[T], error) {
	ctx, span := d.tracer.Start(ctx, d.name+".List")
	defer span.End()
	span.SetAttribute("db.repository", d.name)
	span.SetAttribute("db.operation", "List")
	result, err := d.inner.List(ctx, params, page)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(interfaces.SpanStatusError, err.Error())
	}
	return result, err
}

// Create wraps the inner Create in a span, recording the error and error
// status on the span when the operation fails.
func (d *tracingDecorator[T, P, ID]) Create(ctx context.Context, entity *T) (*T, error) {
	ctx, span := d.tracer.Start(ctx, d.name+".Create")
	defer span.End()
	span.SetAttribute("db.repository", d.name)
	span.SetAttribute("db.operation", "Create")
	result, err := d.inner.Create(ctx, entity)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(interfaces.SpanStatusError, err.Error())
	}
	return result, err
}

// Update wraps the inner Update in a span, recording the error and error
// status on the span when the operation fails.
func (d *tracingDecorator[T, P, ID]) Update(
	ctx context.Context,
	id ID,
	entity *T,
) (*T, error) {
	ctx, span := d.tracer.Start(ctx, d.name+".Update")
	defer span.End()
	span.SetAttribute("db.repository", d.name)
	span.SetAttribute("db.operation", "Update")
	result, err := d.inner.Update(ctx, id, entity)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(interfaces.SpanStatusError, err.Error())
	}
	return result, err
}

// Delete wraps the inner Delete in a span, recording the error and error
// status on the span when the operation fails.
func (d *tracingDecorator[T, P, ID]) Delete(ctx context.Context, id ID) error {
	ctx, span := d.tracer.Start(ctx, d.name+".Delete")
	defer span.End()
	span.SetAttribute("db.repository", d.name)
	span.SetAttribute("db.operation", "Delete")
	err := d.inner.Delete(ctx, id)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(interfaces.SpanStatusError, err.Error())
	}
	return err
}

// Exists wraps the inner Exists in a span, recording the error and error
// status on the span when the operation fails.
func (d *tracingDecorator[T, P, ID]) Exists(ctx context.Context, id ID) (bool, error) {
	ctx, span := d.tracer.Start(ctx, d.name+".Exists")
	defer span.End()
	span.SetAttribute("db.repository", d.name)
	span.SetAttribute("db.operation", "Exists")
	exists, err := d.inner.Exists(ctx, id)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(interfaces.SpanStatusError, err.Error())
	}
	return exists, err
}
