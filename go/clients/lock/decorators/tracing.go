package decorators

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// compile-time assertion that *tracingDecorator satisfies the seam.
var _ interfaces.DistributedLock = (*tracingDecorator)(nil)

// tracingDecorator opens a client span per lock operation so the Redis
// round-trips are visible in Tempo. It is the outermost of the observability
// trio, so a span covers the whole resilient operation (including retries).
type tracingDecorator struct {
	// inner is the next lock in the decorator chain.
	inner interfaces.DistributedLock
	// tracer creates the spans.
	tracer interfaces.Tracer
	// name is the span-name prefix and a span attribute.
	name string
}

// Acquire wraps the inner Acquire in a "<name>.Acquire" client span, tagging the
// key + acquired outcome and recording any error.
func (d *tracingDecorator) Acquire(
	ctx context.Context,
	key string,
) (token string, acquired bool, err error) {
	ctx, span := d.tracer.Start(
		ctx,
		d.name+".Acquire",
		interfaces.WithSpanKind(interfaces.SpanKindClient),
	)
	defer span.End()
	span.SetAttribute("lock.name", d.name)
	span.SetAttribute("lock.operation", "acquire")
	span.SetAttribute("lock.key", key)
	token, acquired, err = d.inner.Acquire(ctx, key)
	span.SetAttribute("lock.acquired", acquired)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(interfaces.SpanStatusError, err.Error())
	}
	return token, acquired, err
}

// Renew wraps the inner Renew in a "<name>.Renew" client span, tagging the key +
// held outcome and recording any error.
func (d *tracingDecorator) Renew(ctx context.Context, key, token string) (bool, error) {
	ctx, span := d.tracer.Start(
		ctx,
		d.name+".Renew",
		interfaces.WithSpanKind(interfaces.SpanKindClient),
	)
	defer span.End()
	span.SetAttribute("lock.name", d.name)
	span.SetAttribute("lock.operation", "renew")
	span.SetAttribute("lock.key", key)
	held, err := d.inner.Renew(ctx, key, token)
	span.SetAttribute("lock.held", held)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(interfaces.SpanStatusError, err.Error())
	}
	return held, err
}

// Release wraps the inner Release in a "<name>.Release" client span, recording
// any error.
func (d *tracingDecorator) Release(ctx context.Context, key, token string) error {
	ctx, span := d.tracer.Start(
		ctx,
		d.name+".Release",
		interfaces.WithSpanKind(interfaces.SpanKindClient),
	)
	defer span.End()
	span.SetAttribute("lock.name", d.name)
	span.SetAttribute("lock.operation", "release")
	span.SetAttribute("lock.key", key)
	err := d.inner.Release(ctx, key, token)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(interfaces.SpanStatusError, err.Error())
	}
	return err
}
