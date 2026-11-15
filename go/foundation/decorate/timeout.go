package decorate

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
)

// NewTimeout returns a middleware that bounds each operation with a per-op deadline and
// codes an overrun as CodeTimeout, so a deadline the layer set never surfaces as an
// uncoded context error. A non-positive duration is a passthrough.
//
// An overrun the layer's own deadline caused is reported through obs. One the caller's
// earlier deadline caused is left to the caller, and an unrelated error returned after
// the deadline passed is not counted.
func NewTimeout(timeout time.Duration, obs Observer) OpMiddleware {
	return MiddlewareFunc(
		func(parent context.Context, operation string, next func(context.Context) error) error {
			if timeout <= 0 {
				return next(parent)
			}

			ctx, cancel := context.WithTimeout(parent, timeout)

			defer cancel()

			err := next(ctx)
			if !deadlineError(err) {
				return err
			}

			return timedOut(parent, ctx, &obs, operation, err)
		},
	)
}

// timedOut reports a deadline overrun through obs when the layer's own deadline (ctx's,
// derived from parent) caused it, and returns err coded CodeTimeout.
func timedOut(
	parent, ctx context.Context,
	obs *Observer,
	operation string,
	err error,
) error {
	if ownDeadline(parent, ctx) {
		obs.Recorder.TimedOut(operation)
	}

	if apperr.Code(err) == apperr.CodeTimeout {
		return err
	}

	return apperr.Wrap(err, apperr.CodeTimeout, operation+" timed out")
}

// deadlineError reports whether err is a deadline overrun: the context's own error or
// one already coded CodeTimeout.
func deadlineError(err error) bool {
	return err != nil &&
		(apperr.StdIs(err, context.DeadlineExceeded) || apperr.Code(err) == apperr.CodeTimeout)
}

// ownDeadline reports whether ctx, derived from parent by the timeout layer, expired on
// the layer's deadline rather than on an earlier one parent already carried.
func ownDeadline(parent, ctx context.Context) bool {
	if !apperr.StdIs(ctx.Err(), context.DeadlineExceeded) {
		return false
	}

	own, _ := ctx.Deadline()
	inherited, ok := parent.Deadline()

	return !ok || inherited.After(own)
}
