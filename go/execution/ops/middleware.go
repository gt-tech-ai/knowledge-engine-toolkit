package ops

import (
	"context"
	"fmt"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// decorators returns the chain every run of the operation d describes passes through,
// outermost first (charter §6.3): recovery → timeout → logging → code map. Logging sits
// outside the code map, so a logged failure carries the code the caller gets; it is
// skipped when log is nil.
func decorators(
	d types.OperationDescriptor,
	log interfaces.Logger,
) decorate.OpMiddleware {
	mws := []decorate.OpMiddleware{
		decorate.NewRecovery(log, "operation", d.Name),
		decorate.NewTimeout(d.Timeout),
	}
	if log != nil {
		mws = append(mws, decorate.NewLogging(log, "operation", d.Name))
	}
	return decorate.Chain(append(mws, codeMap(d))...)
}

// codeMap classifies a failed run by its context: past the deadline is CodeTimeout
// naming the operation and its timeout, a cancelled context is CodeCanceled, and any
// other error passes through with its own code, so a front-end's exit codes don't shift.
func codeMap(d types.OperationDescriptor) decorate.OpMiddleware {
	return decorate.MiddlewareFunc(
		func(ctx context.Context, _ string, next func(context.Context) error) error {
			err := next(ctx)
			if err == nil {
				return nil
			}
			switch {
			case apperr.StdIs(ctx.Err(), context.DeadlineExceeded):
				return apperr.Wrap(err, apperr.CodeTimeout, fmt.Sprintf(
					"operation %s exceeded its %s timeout", d.Name, d.Timeout,
				))
			case apperr.StdIs(ctx.Err(), context.Canceled):
				return apperr.Wrap(err, apperr.CodeCanceled, fmt.Sprintf(
					"operation %s was canceled", d.Name,
				))
			}
			return err
		},
	)
}
