// Package interceptorcore holds the framework-agnostic resilience decisions
// shared by the gRPC and Connect interceptor stacks. The two stacks keep their
// own packages — the two-stack split is intentional and stays — and each
// becomes a thin adapter that translates its framework's call signature and
// status codes to/from the primitives here. The retry classification, the
// circuit-breaker gate, the bulkhead load-shed decision, and the timeout
// classification therefore live once instead of being re-implemented per stack.
//
// The framework-specific pieces stay in the adapters: the call closure (which
// invokes the framework's next/invoker/streamer), the code map that decides
// which errors are permanent, and the construction of the framework's error
// (gRPC status vs Connect error). This package depends only on core/interfaces
// and the stdlib, so it sits cleanly in the clients tier below both stacks.
package interceptorcore

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Retry runs call under retrier, treating an error for which isPermanent reports
// true as terminal: it is captured and returned as permanent (never retried) and
// the retry loop stops. A retryable error is retried until the retrier gives up,
// whereupon the retrier's error is returned as exhausted. On success both are nil.
//
// permanent and exhausted are mutually exclusive — a permanent error makes the
// retry closure report success to the retrier (so retryErr is nil), and an
// exhausted retry never sets permanent. The adapter maps permanent to a verbatim
// return and exhausted to its framework's Unavailable code. This is the exact
// classification both stacks' retry interceptors performed inline.
func Retry(
	ctx context.Context,
	retrier interfaces.Retrier,
	isPermanent func(error) bool,
	call func() error,
) (permanent, exhausted error) {
	retryErr := retrier.Retry(ctx, func() error {
		err := call()
		if err == nil {
			return nil
		}
		if isPermanent(err) {
			permanent = err
			return nil // stop retrying — this error is terminal
		}
		return err // retryable
	})
	if permanent != nil {
		return permanent, nil
	}
	return nil, retryErr
}

// CircuitBreak runs call under cb. It returns rejected=true when the breaker
// refused the call before it ran (circuit open) — the adapter maps that to
// Unavailable; otherwise innerErr carries the call's own error (nil on success).
// The internal ran flag distinguishes an open-circuit rejection from the call's
// own error without depending on the breaker's internal full-sentinel, keeping
// the adapter on the CircuitBreaker interface rather than its implementation.
func CircuitBreak(
	cb interfaces.CircuitBreaker,
	call func() error,
) (rejected bool, innerErr error) {
	ran := false
	cbErr := cb.Execute(func() error {
		ran = true
		innerErr = call()
		return innerErr
	})
	if cbErr != nil && !ran {
		return true, nil
	}
	return false, innerErr
}

// Bulkhead runs call under bh only when a concurrency slot is free. It returns
// ran=false when load was shed (call never ran), with err the bulkhead's
// rejection error — the adapter maps that to ResourceExhausted; otherwise ran=true
// and err is the call's own error. The ran flag keeps the adapter on the Bulkhead
// interface, not its internal full-sentinel.
func Bulkhead(bh interfaces.Bulkhead, call func() error) (ran bool, err error) {
	shedErr := bh.TryExecute(func() error {
		ran = true
		err = call()
		return err
	})
	if !ran {
		return false, shedErr
	}
	return true, err
}

// TimeoutClass classifies why a deadline-bounded call failed.
type TimeoutClass int

const (
	// TimeoutNone means the failure was not a deadline or cancellation; the
	// adapter returns the call's original error unchanged.
	TimeoutNone TimeoutClass = iota
	// TimeoutDeadline means the interceptor's own deadline elapsed; the adapter
	// reports the actual elapsed time, not the nominal timeout.
	TimeoutDeadline
	// TimeoutCanceled means ctx was canceled by the caller or a shorter parent
	// deadline — a fast-fail that must NOT be relabeled as a full timeout wait.
	TimeoutCanceled
)

// ClassifyTimeout inspects ctx after a failed deadline-bounded call and reports
// whether the failure was the interceptor's own deadline, an upstream
// cancellation, or neither. Distinguishing the deadline from a cancellation
// prevents a fast-fail on an already-cancelled (or shorter-parent) context from
// being mislabeled as a full timeout wait, which would corrupt latency diagnosis.
// The adapter builds its framework error from the returned class and the elapsed
// time.
func ClassifyTimeout(ctx context.Context) TimeoutClass {
	switch ctx.Err() {
	case context.DeadlineExceeded:
		return TimeoutDeadline
	case context.Canceled:
		return TimeoutCanceled
	default:
		return TimeoutNone
	}
}
