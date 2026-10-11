package decorate

import (
	"context"
	"fmt"
	"runtime/debug"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// NewRecovery returns a middleware that recovers a panic in the operation, logs it
// with a stack trace (when logger is non-nil), and returns a coded Internal error
// instead of letting the panic unwind. name labels the log entry. Recovery is the
// outermost middleware in a chain that uses it (the service tier); the repo tier has
// no recovery decorator, matching its builder.
func NewRecovery(logger interfaces.Logger, name string) OpMiddleware {
	return MiddlewareFunc(
		func(ctx context.Context, _ string, next func(context.Context) error) (err error) {
			defer func() {
				if r := recover(); r != nil {
					if logger != nil {
						logger.WithContext(ctx).Error(
							"service panic recovered",
							"service", name,
							"panic", fmt.Sprint(r),
							"stack", string(debug.Stack()),
						)
					}
					err = apperr.Internal("internal service error: panic recovered")
				}
			}()
			return next(ctx)
		},
	)
}
