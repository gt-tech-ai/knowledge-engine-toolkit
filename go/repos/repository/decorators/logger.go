package decorators

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// loggingDecorator logs repository operations at Debug level on entry and
// Debug level on failure (suppressed in staging/prod where level=info).
type loggingDecorator[T any, P any, ID comparable] struct {
	// inner is the next repository in the decorator chain.
	inner interfaces.DecoratedRepository[T, P, ID]

	// logger is the structured logger used for all log entries.
	logger interfaces.Logger

	// name is the repository name included in every log entry.
	name string
}

// Get logs the lookup and any failure, then delegates to the inner repository.
func (d *loggingDecorator[T, P, ID]) Get(ctx context.Context, id ID) (*T, error) {
	d.logger.WithContext(ctx).Debug("repository.Get", "repo", d.name, "id", id)
	result, err := d.inner.Get(ctx, id)
	if err != nil {
		d.logger.WithContext(ctx).
			Debug("repository.Get failed", "repo", d.name, "id", id, "error", err)
	}
	return result, err
}

// List logs the query and any failure, then delegates to the inner repository.
func (d *loggingDecorator[T, P, ID]) List(
	ctx context.Context,
	params P,
	page types.PageRequest,
) (*types.Page[T], error) {
	d.logger.WithContext(ctx).Debug("repository.List", "repo", d.name)
	result, err := d.inner.List(ctx, params, page)
	if err != nil {
		d.logger.WithContext(ctx).
			Debug("repository.List failed", "repo", d.name, "error", err)
	}
	return result, err
}

// Create logs the operation and any failure, then delegates to the inner
// repository.
func (d *loggingDecorator[T, P, ID]) Create(ctx context.Context, entity *T) (*T, error) {
	d.logger.WithContext(ctx).Debug("repository.Create", "repo", d.name)
	result, err := d.inner.Create(ctx, entity)
	if err != nil {
		d.logger.WithContext(ctx).
			Debug("repository.Create failed", "repo", d.name, "error", err)
	}
	return result, err
}

// Update logs the operation and any failure, then delegates to the inner
// repository.
func (d *loggingDecorator[T, P, ID]) Update(
	ctx context.Context,
	id ID,
	entity *T,
) (*T, error) {
	d.logger.WithContext(ctx).Debug("repository.Update", "repo", d.name, "id", id)
	result, err := d.inner.Update(ctx, id, entity)
	if err != nil {
		d.logger.WithContext(ctx).
			Debug("repository.Update failed", "repo", d.name, "id", id, "error", err)
	}
	return result, err
}

// Delete logs the operation and any failure, then delegates to the inner
// repository.
func (d *loggingDecorator[T, P, ID]) Delete(ctx context.Context, id ID) error {
	d.logger.WithContext(ctx).Debug("repository.Delete", "repo", d.name, "id", id)
	err := d.inner.Delete(ctx, id)
	if err != nil {
		d.logger.WithContext(ctx).
			Debug("repository.Delete failed", "repo", d.name, "id", id, "error", err)
	}
	return err
}

// Exists logs the check and any failure, then delegates to the inner
// repository.
func (d *loggingDecorator[T, P, ID]) Exists(ctx context.Context, id ID) (bool, error) {
	d.logger.WithContext(ctx).Debug("repository.Exists", "repo", d.name, "id", id)
	exists, err := d.inner.Exists(ctx, id)
	if err != nil {
		d.logger.WithContext(ctx).
			Debug("repository.Exists failed", "repo", d.name, "id", id, "error", err)
	}
	return exists, err
}
