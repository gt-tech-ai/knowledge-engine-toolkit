package decorators

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// compile-time assertion that *timeoutDecorator satisfies the seam.
var _ interfaces.ReplayBuffer = (*timeoutDecorator)(nil)

// timeoutDecorator enforces a per-operation deadline by bounding the context, so a wedged
// Redis cannot stall the caller — critical on the write pump's hot Append path. It is the
// innermost decorator, nesting closest to the backend.
type timeoutDecorator struct {
	// inner is the next buffer in the decorator chain.
	inner interfaces.ReplayBuffer
	// timeout is the per-operation deadline applied to each call.
	timeout time.Duration
}

// Append runs the inner Append under a deadline-bounded context.
func (d *timeoutDecorator) Append(
	ctx context.Context,
	key, msgID string,
	payload []byte,
) error {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.inner.Append(ctx, key, msgID, payload)
}

// ReplayAfter runs the inner ReplayAfter under a deadline-bounded context.
func (d *timeoutDecorator) ReplayAfter(
	ctx context.Context,
	key, afterMsgID string,
) ([]interfaces.BufferedMessage, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.inner.ReplayAfter(ctx, key, afterMsgID)
}

// Prune runs the inner Prune under a deadline-bounded context.
func (d *timeoutDecorator) Prune(ctx context.Context, key, upToMsgID string) error {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.inner.Prune(ctx, key, upToMsgID)
}

// Delete runs the inner Delete under a deadline-bounded context.
func (d *timeoutDecorator) Delete(ctx context.Context, key string) error {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.inner.Delete(ctx, key)
}
