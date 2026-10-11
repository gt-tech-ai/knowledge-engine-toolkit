// Package decorators wraps the messaging MessagePublisher and MessageHandler with
// cross-cutting concerns. The publisher routes each send through the shared
// client-boundary resilience stack (go/clients/decorators) —
// Bulkhead → Retry → CircuitBreaker → Timeout → Tracing → Metrics → Logging — so
// the direct-SDK SendMessage path gets the same protection as the other clients
// (DRY — one stack, every client). The existing Retrier is reused as the stack's
// Retry layer (never a second retry). Per-message handler observability (which is
// NOT a direct-SDK boundary) stays in handler.go by design.
package decorators

import (
	"context"
	"time"

	oteltrace "go.opentelemetry.io/otel/trace"

	clientstack "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/decorators"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// PublisherDeps carries the optional collaborators used to decorate a publisher.
// A nil field (or zero Timeout) disables that layer, so callers opt in to exactly
// the concerns they have wired.
type PublisherDeps struct {
	// Logger records failed publishes; nil disables the failure log.
	Logger interfaces.Logger

	// Metrics records per-publish counts, errors, and latency; nil disables metrics.
	Metrics interfaces.Metrics

	// Retrier is reused as the stack's Retry layer (never a second retry); nil disables
	// retry.
	Retrier interfaces.Retrier

	// Bulkhead bounds concurrent publishes; nil disables it.
	Bulkhead interfaces.Bulkhead

	// CircuitBreaker fails a publish fast when the broker is down; nil disables it.
	CircuitBreaker interfaces.CircuitBreaker

	// Tracer emits a span per publish; nil disables tracing.
	Tracer oteltrace.Tracer

	// Name labels the publisher in metrics/spans; empty defaults to "messaging-publisher".
	Name string

	// Timeout is the per-publish deadline; zero disables the timeout layer.
	Timeout time.Duration
}

// WrapPublisher composes the shared resilience stack around core. The Retrier is
// reused as the stack's Retry layer, so a publish is retried per the Retrier's
// policy and never double-retried.
func WrapPublisher(
	core interfaces.MessagePublisher, deps PublisherDeps,
) interfaces.MessagePublisher {
	name := deps.Name
	if name == "" {
		name = "messaging-publisher"
	}
	stack := clientstack.New(name).
		WithBulkhead(deps.Bulkhead).
		WithRetrier(deps.Retrier).
		WithCircuitBreaker(deps.CircuitBreaker).
		WithTimeout(deps.Timeout).
		WithTracer(deps.Tracer).
		WithMetrics(deps.Metrics).
		WithLogger(deps.Logger)
	return &stackPublisher{core: core, stack: stack}
}

// stackPublisher runs each publish through the shared resilience stack, labelled by
// topic so per-topic publish rates/failures remain visible in metrics/spans.
type stackPublisher struct {
	// core is the wrapped backend publisher (e.g. the SQS SendMessage adapter).
	core interfaces.MessagePublisher

	// stack is the shared resilience+observability stack each publish runs through.
	stack *clientstack.Stack
}

// Compile-time assertion that stackPublisher satisfies MessagePublisher.
var _ interfaces.MessagePublisher = (*stackPublisher)(nil)

// publishRetryable returns the flags that enable the stack's Retry layer: an SQS
// SendMessage is safe to retry under at-least-once delivery.
func publishRetryable() clientstack.RunOpts {
	return clientstack.RunOpts{Retryable: true}
}

// Publish sends payload to topic through the resilience stack.
func (p *stackPublisher) Publish(
	ctx context.Context,
	topic string,
	payload []byte,
) error {
	_, err := clientstack.Run(ctx, p.stack, topic, publishRetryable(),
		func(c context.Context) (struct{}, error) {
			return struct{}{}, p.core.Publish(c, topic, payload)
		})
	return err
}

// PublishBatch sends payloads to topic through the resilience stack.
func (p *stackPublisher) PublishBatch(
	ctx context.Context,
	topic string,
	payloads [][]byte,
) error {
	_, err := clientstack.Run(ctx, p.stack, topic, publishRetryable(),
		func(c context.Context) (struct{}, error) {
			return struct{}{}, p.core.PublishBatch(c, topic, payloads)
		})
	return err
}
