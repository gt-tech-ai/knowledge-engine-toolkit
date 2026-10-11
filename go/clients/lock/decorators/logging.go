package decorators

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// compile-time assertion that *loggingDecorator satisfies the seam.
var _ interfaces.DistributedLock = (*loggingDecorator)(nil)

// loggingDecorator logs each lock operation at Debug with its outcome and any
// failure at Error, using the structured Logger. It is the innermost of the
// observability trio.
type loggingDecorator struct {
	// inner is the next lock in the decorator chain.
	inner interfaces.DistributedLock
	// logger writes the structured entries.
	logger interfaces.Logger
	// name labels the lock instance in every entry.
	name string
}

// Acquire logs the acquire outcome (acquired/contended) or the failure. It binds the
// request context onto the logger (WithContext) so every entry carries the active
// trace/span ids — the tracing decorator sits outside this one, so ctx already holds
// the span. Matches the transport interceptors' logging (interceptors/logging.go).
func (d *loggingDecorator) Acquire(
	ctx context.Context,
	key string,
) (token string, acquired bool, err error) {
	token, acquired, err = d.inner.Acquire(ctx, key)
	log := d.logger.WithContext(ctx)
	if err != nil {
		log.Error("lock acquire failed", "lock", d.name, "key", key, "error", err)
		return token, acquired, err
	}
	log.Debug("lock acquire", "lock", d.name, "key", key, "acquired", acquired)
	return token, acquired, err
}

// Renew logs the renew outcome (held/lost) or the failure, trace-correlated via ctx.
func (d *loggingDecorator) Renew(ctx context.Context, key, token string) (bool, error) {
	held, err := d.inner.Renew(ctx, key, token)
	log := d.logger.WithContext(ctx)
	if err != nil {
		log.Error("lock renew failed", "lock", d.name, "key", key, "error", err)
		return held, err
	}
	log.Debug("lock renew", "lock", d.name, "key", key, "held", held)
	return held, err
}

// Release logs the release or the failure, trace-correlated via ctx.
func (d *loggingDecorator) Release(ctx context.Context, key, token string) error {
	err := d.inner.Release(ctx, key, token)
	log := d.logger.WithContext(ctx)
	if err != nil {
		log.Error("lock release failed", "lock", d.name, "key", key, "error", err)
		return err
	}
	log.Debug("lock release", "lock", d.name, "key", key)
	return err
}
