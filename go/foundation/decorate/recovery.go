package decorate

import (
	"context"
	"fmt"
	"runtime/debug"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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
					err = recoveredError(ctx, logger, tier, name, op, r)
				}
			}()

			return next(ctx)
		},
	)
}

// recoveredError logs a recovered panic, when a logger is set, and returns the coded
// error the caller sees.
func recoveredError(
	ctx context.Context,
	logger interfaces.Logger,
	tier, name, operation string,
	value any,
) error {
	if logger != nil {
		logger.WithContext(ctx).Error(
			tier+" panic recovered",
			tier, name,
			textOperation, operation,
			"panic", fmt.Sprint(value),
			"stack", string(debug.Stack()),
		)
	}

	return apperr.Internal("internal " + tier + " error: panic recovered")
}
