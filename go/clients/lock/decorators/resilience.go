package decorators

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// ---------------------------------------------------------------------------
// timeoutDecorator — bounds each operation with a per-call deadline.
// ---------------------------------------------------------------------------

// compile-time assertion that *timeoutDecorator satisfies the seam.
var _ interfaces.DistributedLock = (*timeoutDecorator)(nil)

// timeoutDecorator enforces a per-operation deadline by bounding the context.
type timeoutDecorator struct {
	// inner is the next lock in the decorator chain.
	inner interfaces.DistributedLock
	// timeout is the per-operation deadline applied to each call.
	timeout time.Duration
}

// Acquire runs the inner Acquire under a deadline-bounded context.
func (d *timeoutDecorator) Acquire(
	ctx context.Context,
	key string,
) (token string, acquired bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.inner.Acquire(ctx, key)
}

// Renew runs the inner Renew under a deadline-bounded context.
func (d *timeoutDecorator) Renew(ctx context.Context, key, token string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.inner.Renew(ctx, key, token)
}

// Release runs the inner Release under a deadline-bounded context.
func (d *timeoutDecorator) Release(ctx context.Context, key, token string) error {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.inner.Release(ctx, key, token)
}

// ---------------------------------------------------------------------------
// retryDecorator — retries an operation on a transient backend error.
// ---------------------------------------------------------------------------

// compile-time assertion that *retryDecorator satisfies the seam.
var _ interfaces.DistributedLock = (*retryDecorator)(nil)

// retryDecorator retries an operation when the backend returns a (transient)
// error. Contention — Acquire returning acquired=false with a nil error — is NOT
// an error and is never retried, so a try-lock stays non-blocking.
type retryDecorator struct {
	// inner is the next lock in the decorator chain.
	inner interfaces.DistributedLock
	// retrier applies the backoff policy to each retried call.
	retrier interfaces.Retrier
}

// Acquire retries the inner Acquire on a transient error, returning the last
// attempt's token/acquired.
func (d *retryDecorator) Acquire(
	ctx context.Context,
	key string,
) (token string, acquired bool, err error) {
	err = d.retrier.Retry(ctx, func() error {
		var e error
		token, acquired, e = d.inner.Acquire(ctx, key)
		return e
	})
	return token, acquired, err
}

// Renew retries the inner Renew on a transient error, returning the last
// attempt's held.
func (d *retryDecorator) Renew(ctx context.Context, key, token string) (bool, error) {
	var held bool
	err := d.retrier.Retry(ctx, func() error {
		var e error
		held, e = d.inner.Renew(ctx, key, token)
		return e
	})
	return held, err
}

// Release retries the inner Release on a transient error.
func (d *retryDecorator) Release(ctx context.Context, key, token string) error {
	return d.retrier.Retry(ctx, func() error {
		return d.inner.Release(ctx, key, token)
	})
}

// ---------------------------------------------------------------------------
// circuitBreakerDecorator — trips on repeated backend failure, fail-closed.
// ---------------------------------------------------------------------------

// compile-time assertion that *circuitBreakerDecorator satisfies the seam.
var _ interfaces.DistributedLock = (*circuitBreakerDecorator)(nil)

// circuitBreakerDecorator gates operations with a circuit breaker. When the
// circuit is open it surfaces the error (fail-closed) and never fabricates a
// result: a fake acquired=true would admit a duplicate operation and a fake
// acquired=false would wedge every pod, so the caller must decide how to degrade.
type circuitBreakerDecorator struct {
	// inner is the next lock in the decorator chain.
	inner interfaces.DistributedLock
	// cb gates each operation, short-circuiting when the circuit is open.
	cb interfaces.CircuitBreaker
}

// Acquire runs the inner Acquire through the breaker; on open/error it surfaces
// the error without fabricating acquired.
func (d *circuitBreakerDecorator) Acquire(
	ctx context.Context,
	key string,
) (token string, acquired bool, err error) {
	err = d.cb.Execute(func() error {
		var e error
		token, acquired, e = d.inner.Acquire(ctx, key)
		return e
	})
	if err != nil {
		return "", false, err
	}
	return token, acquired, nil
}

// Renew runs the inner Renew through the breaker; on open/error it surfaces the
// error (the watchdog treats that as a lost lease, the fail-safe direction).
func (d *circuitBreakerDecorator) Renew(
	ctx context.Context,
	key, token string,
) (bool, error) {
	var held bool
	err := d.cb.Execute(func() error {
		var e error
		held, e = d.inner.Renew(ctx, key, token)
		return e
	})
	if err != nil {
		return false, err
	}
	return held, nil
}

// Release runs the inner Release through the breaker.
func (d *circuitBreakerDecorator) Release(ctx context.Context, key, token string) error {
	return d.cb.Execute(func() error {
		return d.inner.Release(ctx, key, token)
	})
}
