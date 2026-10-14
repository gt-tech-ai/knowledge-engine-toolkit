package decorate

import (
	"context"
	"fmt"
	"runtime/debug"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// NewRecovery returns a middleware that recovers a panic in the operation, logs it
// with its stack (when logger is non-nil), and returns a coded INTERNAL error that
// carries no panic detail, instead of letting the panic unwind. Recovery is the
// outermost layer of every tier that has it.
func NewRecovery(logger interfaces.Logger, tier, name string) OpMiddleware {
	return MiddlewareFunc(
		func(ctx context.Context, op string, next func(context.Context) error) (err error) {
			defer func() {
				if r := recover(); r != nil {
					if logger != nil {
						logger.WithContext(ctx).Error(
							tier+" panic recovered",
							tier, name,
							"operation", op,
							"panic", fmt.Sprint(r),
							"stack", string(debug.Stack()),
						)
					}
					err = apperr.Internal("internal " + tier + " error: panic recovered")
				}
			}()
			return next(ctx)
		},
	)
}
