package decorators

import (
	"context"
	"fmt"
	"runtime/debug"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// recoveryDecorator catches panics in any inner decorator or the base service
// and converts them to CodeInternal errors. It is always the outermost
// decorator in the chain to ensure no panic escapes the service boundary.
type recoveryDecorator[T any, P any, ID comparable] struct {
	// inner is the next service in the decorator chain.
	inner interfaces.DecoratedService[T, P, ID]

	// logger is used to log recovered panics with stack traces. May be nil.
	logger interfaces.Logger

	// name is the service name included in panic log entries.
	name string
}

// Get delegates to the inner service, recovering any panic into a CodeInternal error.
func (d *recoveryDecorator[T, P, ID]) Get(
	ctx context.Context,
	id ID,
) (result *T, err error) {
	defer d.recoverPanic(ctx, &err)
	return d.inner.Get(ctx, id)
}

// List delegates to the inner service, recovering any panic into a CodeInternal error.
func (d *recoveryDecorator[T, P, ID]) List(
	ctx context.Context,
	params P,
	page types.PageRequest,
) (result *types.Page[T], err error) {
	defer d.recoverPanic(ctx, &err)
	return d.inner.List(ctx, params, page)
}

// Create delegates to the inner service, recovering any panic into a CodeInternal error.
func (d *recoveryDecorator[T, P, ID]) Create(
	ctx context.Context,
	entity *T,
) (result *T, err error) {
	defer d.recoverPanic(ctx, &err)
	return d.inner.Create(ctx, entity)
}

// Update delegates to the inner service, recovering any panic into a CodeInternal error.
func (d *recoveryDecorator[T, P, ID]) Update(
	ctx context.Context,
	id ID,
	entity *T,
) (result *T, err error) {
	defer d.recoverPanic(ctx, &err)
	return d.inner.Update(ctx, id, entity)
}

// Delete delegates to the inner service, recovering any panic into a CodeInternal error.
func (d *recoveryDecorator[T, P, ID]) Delete(ctx context.Context, id ID) (err error) {
	defer d.recoverPanic(ctx, &err)
	return d.inner.Delete(ctx, id)
}

// recoverPanic catches a panic, logs it with a stack trace, and sets the error
// pointer to a CodeInternal AppError.
func (d *recoveryDecorator[T, P, ID]) recoverPanic(
	ctx context.Context,
	err *error, //nolint:gocritic // pointer to error is intentional for defer pattern
) {
	if r := recover(); r != nil {
		stack := debug.Stack()
		if d.logger != nil {
			d.logger.WithContext(ctx).Error(
				"service panic recovered",
				"service", d.name,
				"panic", fmt.Sprint(r),
				"stack", string(stack),
			)
		}
		*err = apperr.Internal("internal service error: panic recovered")
	}
}
