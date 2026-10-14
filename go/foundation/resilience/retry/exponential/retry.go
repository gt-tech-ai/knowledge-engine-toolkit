// Package exponential provides an exponential backoff retrier using cenkalti/backoff/v5.
package exponential

import (
	"context"
	"time"

	"github.com/cenkalti/backoff/v5"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/budget"
)

// Compile-time interface assertion.
var _ interfaces.Retrier = (*Retrier)(nil)

// Config holds exponential backoff settings.
type Config struct {
	// IsRetryable classifies whether a failed operation's error is worth
	// retrying. When nil, defaultIsRetryable is used. Return false for
	// permanent failures so they are surfaced immediately instead of retried.
	IsRetryable func(error) bool

	// MaxRetries is the maximum number of retry attempts before giving up.
	MaxRetries int

	// InitialInterval is the first backoff delay between retries.
	InitialInterval time.Duration

	// MaxInterval caps the backoff delay regardless of the multiplier.
	MaxInterval time.Duration

	// Multiplier scales the backoff interval after each retry attempt.
	Multiplier float64

	// MaxElapsedTime is the absolute deadline for all retries combined.
	MaxElapsedTime time.Duration
}

// defaultIsRetryable reports whether an error is worth retrying. Permanent
// client/domain failures — constraint violations (CodeConflict), invalid input,
// not-found, and auth errors — are never transient: retrying them wastes time
// and, inside a transaction, masks the real cause behind SQLSTATE 25P02. A spent
// quota (CodeResourceExhausted) is not retried either: it recovers only when its
// window resets, never within a backoff. A caller-cancelled context is likewise
// permanent for this call. All other errors (transient DB/network failures,
// unknown errors) are retryable.
func defaultIsRetryable(err error) bool {
	// A caller-cancelled context is permanent for this call: the caller has gone
	// away, so retrying is pointless — and because the circuit breaker shares this
	// policy via IsRetryable, a client cancellation must not count as a breaker
	// failure and trip it against a healthy dependency. DeadlineExceeded stays
	// retryable: a dependency that timed out is a legitimate transient signal.
	if apperr.StdIs(err, context.Canceled) {
		return false
	}
	switch apperr.Code(err) {
	case apperr.CodeConflict,
		apperr.CodeInvalidInput,
		apperr.CodeNotFound,
		apperr.CodeForbidden,
		apperr.CodeUnauthorized,
		apperr.CodeResourceExhausted:
		return false
	case apperr.CodeUnknown,
		apperr.CodeInternal,
		apperr.CodeTimeout,
		apperr.CodeCanceled,
		apperr.CodeUnavailable,
		apperr.CodeIngestion,
		apperr.CodeQualityFailed,
		apperr.CodeUpstream:
		return true
	default:
		return true
	}
}

// IsRetryable reports whether an error is worth retrying under the package's
// default classification: transient infrastructure failures retry; permanent
// client/domain failures (Conflict/InvalidInput/NotFound/Forbidden/Unauthorized)
// and a caller-cancelled context do not. It is exported so other resilience
// primitives — notably the circuit breaker's success classifier — share one
// retryability policy.
func IsRetryable(err error) bool {
	return defaultIsRetryable(err)
}

// DefaultConfig returns sensible defaults for exponential backoff.
func DefaultConfig() Config {
	return Config{
		MaxRetries:      3,
		InitialInterval: 100 * time.Millisecond,
		MaxInterval:     5 * time.Second,
		Multiplier:      2.0,
		MaxElapsedTime:  30 * time.Second,
	}
}

// Retrier implements interfaces.Retrier using exponential backoff with jitter.
type Retrier struct {
	// cfg holds the backoff configuration.
	cfg Config
}

// New creates an exponential backoff retrier from the given config.
func New(cfg Config) *Retrier {
	return &Retrier{cfg: cfg}
}

// classify returns the configured retryability predicate, falling back to
// defaultIsRetryable when none is set.
func (r *Retrier) classify() func(error) bool {
	if r.cfg.IsRetryable != nil {
		return r.cfg.IsRetryable
	}
	return defaultIsRetryable
}

// Retry executes the operation with exponential backoff and jitter.
// It consumes from the context's retry budget (if present) before each attempt.
func (r *Retrier) Retry(ctx context.Context, op func() error) error {
	isRetryable := r.classify()
	_, err := backoff.Retry(ctx, func() (struct{}, error) {
		if !budget.ConsumeRetry(ctx) {
			return struct{}{}, backoff.Permanent(budget.ErrRetryBudgetExhausted)
		}
		if e := op(); e != nil {
			if !isRetryable(e) {
				return struct{}{}, backoff.Permanent(e)
			}
			return struct{}{}, e
		}
		return struct{}{}, nil
	}, r.retryOpts()...)

	return err
}

// RetryWithResult executes an operation that returns a value with exponential backoff.
// Same retry semantics as Retry, but preserves the operation's return value on success.
func RetryWithResult[T any](
	ctx context.Context,
	r *Retrier,
	op func() (T, error),
) (T, error) {
	isRetryable := r.classify()
	return backoff.Retry(ctx, func() (T, error) {
		if !budget.ConsumeRetry(ctx) {
			var zero T
			return zero, backoff.Permanent(budget.ErrRetryBudgetExhausted)
		}
		v, e := op()
		if e != nil && !isRetryable(e) {
			return v, backoff.Permanent(e)
		}
		return v, e
	}, r.retryOpts()...)
}

// retryOpts builds backoff v5 retry options from the stored config.
func (r *Retrier) retryOpts() []backoff.RetryOption {
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = r.cfg.InitialInterval
	b.MaxInterval = r.cfg.MaxInterval
	b.Multiplier = r.cfg.Multiplier
	b.RandomizationFactor = 0.5 // jitter

	opts := []backoff.RetryOption{
		backoff.WithBackOff(b),
		backoff.WithMaxElapsedTime(r.cfg.MaxElapsedTime),
	}
	if r.cfg.MaxRetries > 0 {
		// WithMaxTries counts the first attempt; MaxRetries counts only the retries.
		opts = append(opts, backoff.WithMaxTries(uint(r.cfg.MaxRetries)+1))
	}
	return opts
}
