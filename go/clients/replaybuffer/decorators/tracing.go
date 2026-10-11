package decorators

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// compile-time assertion that *tracingDecorator satisfies the seam.
var _ interfaces.ReplayBuffer = (*tracingDecorator)(nil)

// tracingDecorator opens a client span per buffer operation so the backend round-trips
// are visible in Tempo. It is the outermost decorator, so a span covers the whole bounded
// operation.
type tracingDecorator struct {
	// inner is the next buffer in the decorator chain.
	inner interfaces.ReplayBuffer
	// tracer creates the spans.
	tracer interfaces.Tracer
	// name is the span-name prefix and a span attribute.
	name string
}

// startSpan opens a "<name>.<op>" client span tagged with the buffer + key.
func (d *tracingDecorator) startSpan(
	ctx context.Context,
	op, key string,
) (context.Context, interfaces.Span) {
	ctx, span := d.tracer.Start(
		ctx,
		d.name+"."+op,
		interfaces.WithSpanKind(interfaces.SpanKindClient),
	)
	span.SetAttribute("replaybuffer.name", d.name)
	span.SetAttribute("replaybuffer.operation", op)
	span.SetAttribute("replaybuffer.key", key)
	return ctx, span
}

// recordErr tags a span with the error status when err is non-nil.
func recordErr(span interfaces.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(interfaces.SpanStatusError, err.Error())
	}
}

// Append wraps the inner Append in a client span.
func (d *tracingDecorator) Append(
	ctx context.Context,
	key, msgID string,
	payload []byte,
) error {
	ctx, span := d.startSpan(ctx, "append", key)
	defer span.End()
	err := d.inner.Append(ctx, key, msgID, payload)
	recordErr(span, err)
	return err
}

// ReplayAfter wraps the inner ReplayAfter in a client span.
func (d *tracingDecorator) ReplayAfter(
	ctx context.Context,
	key, afterMsgID string,
) ([]interfaces.BufferedMessage, bool, error) {
	ctx, span := d.startSpan(ctx, "replay_after", key)
	defer span.End()
	msgs, complete, err := d.inner.ReplayAfter(ctx, key, afterMsgID)
	span.SetAttribute("replaybuffer.complete", complete)
	recordErr(span, err)
	return msgs, complete, err
}

// Prune wraps the inner Prune in a client span.
func (d *tracingDecorator) Prune(ctx context.Context, key, upToMsgID string) error {
	ctx, span := d.startSpan(ctx, "prune", key)
	defer span.End()
	err := d.inner.Prune(ctx, key, upToMsgID)
	recordErr(span, err)
	return err
}

// Delete wraps the inner Delete in a client span.
func (d *tracingDecorator) Delete(ctx context.Context, key string) error {
	ctx, span := d.startSpan(ctx, "delete", key)
	defer span.End()
	err := d.inner.Delete(ctx, key)
	recordErr(span, err)
	return err
}
