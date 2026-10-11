package decorators

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// loggingDecorator logs service operations at Debug level on entry and
// Debug level on failure (suppressed in staging/prod where level=info).
type loggingDecorator[T any, P any, ID comparable] struct {
	// inner is the next service in the decorator chain.
	inner interfaces.DecoratedService[T, P, ID]

	// logger is the structured logger used for all log entries.
	logger interfaces.Logger

	// name is the service name included in every log entry.
	name string
}

// Get logs the Get operation on entry and on failure, then delegates to the
// inner service, returning its result unchanged.
func (d *loggingDecorator[T, P, ID]) Get(ctx context.Context, id ID) (*T, error) {
	d.logger.WithContext(ctx).Debug("service.Get", "service", d.name, "id", id)
	result, err := d.inner.Get(ctx, id)
	if err != nil {
		d.logger.WithContext(ctx).
			Debug("service.Get failed", "service", d.name, "id", id, "error", err)
	}
	return result, err
}

// List logs the List operation on entry and on failure, then delegates to the
// inner service, returning its result unchanged.
func (d *loggingDecorator[T, P, ID]) List(
	ctx context.Context,
	params P,
	page types.PageRequest,
) (*types.Page[T], error) {
	d.logger.WithContext(ctx).Debug("service.List", "service", d.name)
	result, err := d.inner.List(ctx, params, page)
	if err != nil {
		d.logger.WithContext(ctx).
			Debug("service.List failed", "service", d.name, "error", err)
	}
	return result, err
}

// Create logs the Create operation on entry and on failure, then delegates to
// the inner service, returning its result unchanged.
func (d *loggingDecorator[T, P, ID]) Create(ctx context.Context, entity *T) (*T, error) {
	d.logger.WithContext(ctx).Debug("service.Create", "service", d.name)
	result, err := d.inner.Create(ctx, entity)
	if err != nil {
		d.logger.WithContext(ctx).
			Debug("service.Create failed", "service", d.name, "error", err)
	}
	return result, err
}

// Update logs the Update operation on entry and on failure, then delegates to
// the inner service, returning its result unchanged.
func (d *loggingDecorator[T, P, ID]) Update(
	ctx context.Context,
	id ID,
	entity *T,
) (*T, error) {
	d.logger.WithContext(ctx).Debug("service.Update", "service", d.name, "id", id)
	result, err := d.inner.Update(ctx, id, entity)
	if err != nil {
		d.logger.WithContext(ctx).
			Debug("service.Update failed", "service", d.name, "id", id, "error", err)
	}
	return result, err
}

// Delete logs the Delete operation on entry and on failure, then delegates to
// the inner service, returning its error unchanged.
func (d *loggingDecorator[T, P, ID]) Delete(ctx context.Context, id ID) error {
	d.logger.WithContext(ctx).Debug("service.Delete", "service", d.name, "id", id)
	err := d.inner.Delete(ctx, id)
	if err != nil {
		d.logger.WithContext(ctx).
			Debug("service.Delete failed", "service", d.name, "id", id, "error", err)
	}
	return err
}
