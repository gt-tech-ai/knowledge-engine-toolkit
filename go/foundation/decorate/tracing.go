package decorate

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// tracingMW opens a span per operation named "<name>.<op>" of the tier's span kind,
// stamps the "<tier>.name" and "<tier>.operation" attributes, and records a failure on
// it.
type tracingMW struct {
	// tracer starts the spans.
	tracer interfaces.Tracer

	// tier prefixes the span attributes.
	tier string

	// name is the decorated subject.
	name string

	// kind is the spans' kind (internal, or client for an outbound call).
	kind interfaces.SpanKind
}

// NewTracing returns the tracing middleware for the subject name in tier, opening spans
// of kind.
func NewTracing(
	tracer interfaces.Tracer,
	tier, name string,
	kind interfaces.SpanKind,
) OpMiddleware {
	return tracingMW{tracer: tracer, kind: kind, tier: tier, name: name}
}

// WrapOp runs next inside the operation's span.
func (m tracingMW) WrapOp(
	ctx context.Context,
	op string,
	next func(context.Context) error,
) error {
	ctx, span := m.tracer.Start(ctx, m.name+"."+op, interfaces.WithSpanKind(m.kind))
	defer span.End()
	span.SetAttribute(m.tier+".name", m.name)
	span.SetAttribute(m.tier+".operation", op)
	err := next(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(interfaces.SpanStatusError, err.Error())
	}
	return err
}
