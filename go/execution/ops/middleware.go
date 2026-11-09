package ops

import (
	"context"
	"fmt"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// decorators returns the chain every run of the operation d describes passes through,
// outermost first: recovery → timeout → logging → code map. Logging sits
// outside the code map, so a logged failure carries the code the caller gets; it is
// skipped when log is nil.
func decorators(
	operationDescriptor *types.OperationDescriptor,
	log interfaces.Logger,
) decorate.OpMiddleware {
	mws := []decorate.OpMiddleware{
		decorate.NewRecovery(log, "operation", operationDescriptor.Name),
		decorate.NewTimeout(operationDescriptor.Timeout),
	}
	if log != nil {
		mws = append(mws, decorate.NewLogging(log, "operation", operationDescriptor.Name))
	}

	return decorate.Chain(append(mws, codeMap(operationDescriptor))...)
}

// codeMap classifies a failed run by its context: past the deadline is CodeTimeout
// naming the operation and its timeout, a cancelled context is CodeCanceled, and any
// other error passes through with its own code, so a front-end's exit codes don't shift.
func codeMap(dRef *types.OperationDescriptor) decorate.OpMiddleware {
	operationDescriptor := *dRef

	return decorate.MiddlewareFunc(
		func(ctx context.Context, _ string, next func(context.Context) error) error {
			err := next(ctx)
			if err == nil {
				return nil
			}

			switch {
			case apperr.StdIs(ctx.Err(), context.DeadlineExceeded):
				return apperr.Wrap(err, apperr.CodeTimeout, fmt.Sprintf(
					"operation %s exceeded its %s timeout",
					operationDescriptor.Name,
					operationDescriptor.Timeout,
				))
			case apperr.StdIs(ctx.Err(), context.Canceled):
				return apperr.Wrap(err, apperr.CodeCanceled, fmt.Sprintf(
					"operation %s was canceled", operationDescriptor.Name,
				))
			}

			return err
		},
	)
}
