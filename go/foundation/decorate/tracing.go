package decorate

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// tracingMW opens a span per operation named "<name>.<op>" and records the error on
// it. subjectAttr/opAttr are the span-attribute keys, so the same middleware serves
// the repo tier ("db.repository"/"db.operation") and the service tier
// ("service.name"/"service.operation").
type tracingMW struct {
	// tracer opens the per-operation span.
	tracer interfaces.Tracer
	// name is the repo/service name — the span-name prefix and subjectAttr value.
	name string
	// subjectAttr is the span-attribute key for the repo/service name.
	subjectAttr string
	// opAttr is the span-attribute key for the operation.
	opAttr string
}

// NewTracing returns a tracing middleware for name, tagging spans with subjectAttr
// (the repo/service name) and opAttr (the operation).
func NewTracing(tracer interfaces.Tracer, name, subjectAttr, opAttr string) OpMiddleware {
	return tracingMW{tracer: tracer, name: name, subjectAttr: subjectAttr, opAttr: opAttr}
}

// WrapOp spans next and records any error on the span.
func (m tracingMW) WrapOp(
	ctx context.Context,
	op string,
	next func(context.Context) error,
) error {
	ctx, span := m.tracer.Start(ctx, m.name+"."+op)
	defer span.End()
	span.SetAttribute(m.subjectAttr, m.name)
	span.SetAttribute(m.opAttr, op)
	err := next(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(interfaces.SpanStatusError, err.Error())
	}
	return err
}
