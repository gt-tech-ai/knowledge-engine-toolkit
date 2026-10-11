// Package nooptracer provides a no-op tracer that satisfies interfaces.Tracer.
// Use it as a fallback when the OTel collector is unavailable (e.g., local dev
// without Docker) or when tracing is explicitly disabled via configuration.
package nooptracer

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Compile-time interface assertions.
var (
	// Tracer must satisfy interfaces.Tracer.
	_ interfaces.Tracer = (*Tracer)(nil)
	// noopSpan must satisfy interfaces.Span.
	_ interfaces.Span = noopSpan{}
)

// Tracer implements interfaces.Tracer as a no-op.
type Tracer struct{}

// New returns a no-op Tracer. The error return is always nil; it exists to
// match the factory signature used by tracer.NewFromConfig.
func New() (*Tracer, error) {
	return &Tracer{}, nil
}

// Start returns the context unchanged and a no-op span.
func (*Tracer) Start(
	ctx context.Context,
	_ string,
	_ ...interfaces.SpanOption,
) (context.Context, interfaces.Span) {
	return ctx, noopSpan{}
}

// Shutdown is a no-op.
func (*Tracer) Shutdown(_ context.Context) error { return nil }

// noopSpan implements interfaces.Span as a no-op.
type noopSpan struct{}

// End is a no-op.
func (noopSpan) End() {}

// SetAttribute discards the attribute.
func (noopSpan) SetAttribute(_ string, _ any) {}

// RecordError discards the error.
func (noopSpan) RecordError(_ error) {}

// SetStatus discards the status.
func (noopSpan) SetStatus(_ interfaces.SpanStatusCode, _ string) {}
