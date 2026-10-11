package e2ekit

import (
	"context"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// pollUntilInterval is how often PollUntil re-checks an async effect.
const pollUntilInterval = 2 * time.Second

// PollUntil calls check every 2s until it returns (true, nil) or the deadline (~timeout)
// elapses, returning the last error/miss. It is the effect analogue of PollTraceInTempo:
// an async worker's "endpoint" is a downstream state change, so a case polls for that
// state.
func PollUntil(
	ctx context.Context,
	timeout time.Duration,
	what string,
	check func(context.Context) (bool, error),
) error {
	return pollEvery(ctx, timeout, pollUntilInterval, what, check)
}

// pollEvery calls check every interval until it reports true or the deadline (~timeout)
// passes. Past the deadline it returns CodeUnavailable, wrapping check's last error when
// there is one; a cancelled ctx returns ctx.Err(). The deadline is checked before each
// wait, so a zero timeout checks once.
func pollEvery(
	ctx context.Context,
	timeout, interval time.Duration,
	what string,
	check func(context.Context) (bool, error),
) error {
	deadline := time.Now().Add(timeout)
	for {
		ok, err := check(ctx)
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return apperr.Wrap(err, apperr.CodeUnavailable, "e2ekit: "+what)
			}
			return apperr.New(
				apperr.CodeUnavailable,
				"e2ekit: "+what+" did not materialize within "+timeout.String(),
			)
		}
		select {
		case <-ctx.Done():
			return apperr.Wrap(
				ctx.Err(),
				apperr.ContextCode(ctx.Err(), apperr.CodeCanceled),
				"e2ekit: poll canceled",
			)
		case <-time.After(interval):
		}
	}
}
