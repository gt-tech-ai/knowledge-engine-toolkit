package decorators

import (
	"context"
	"time"

	oteltrace "go.opentelemetry.io/otel/trace"

	clientstack "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/decorators"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/deadletter"
)

// EventStackDeps carries the collaborators for the EventHandler stack. A nil layer
// (or zero timeout) is skipped, so callers opt into exactly the concerns they have wired.
type EventStackDeps struct {
	// Dedup skips a message whose key was already processed (nil disables dedup).
	Dedup interfaces.Deduplicator

	// KeyOf extracts the dedup key from a message; nil defaults to msg.ID.
	KeyOf func(*interfaces.Message) string

	// DeadLetter routes a message that still fails after retries (nil disables the DLQ).
	DeadLetter *deadletter.Queue

	// Retrier retries a transient handler failure (nil disables retry).
	Retrier interfaces.Retrier

	// CircuitBreaker fails fast when the handler's downstream is down (nil disables it).
	CircuitBreaker interfaces.CircuitBreaker

	// Tracer emits a span per handled message (nil disables tracing).
	Tracer oteltrace.Tracer

	// Metrics records per-message counts/errors/latency (nil disables metrics).
	Metrics interfaces.Metrics

	// Logger records a failed handle at Debug (nil disables the log).
	Logger interfaces.Logger

	// Name labels the handler in metrics and span names.
	Name string

	// Timeout bounds each handle attempt; zero disables the timeout layer.
	Timeout time.Duration
}

// WrapHandler composes the EventHandler stack (ARCHITECTURE.md#decorator-order) around
// inner, outermost → innermost: Dedup → DeadLetter → [Retry → CircuitBreaker → Timeout →
// Tracing → Metrics → Logging] → Handler. (Decode is the handler's own concern.) The
// inner resilience + observability reuses the shared client stack, so the mechanism is
// composed once, not re-implemented. A message that still fails after retries is routed
// to the dead-letter queue and acked when it lands there; pair Dedup with a DeadLetter so
// a terminal failure is dead-lettered rather than skipped as a duplicate on redrive.
func WrapHandler(
	inner interfaces.MessageHandler,
	deps EventStackDeps,
) interfaces.MessageHandler {
	keyOf := deps.KeyOf
	if keyOf == nil {
		keyOf = func(m *interfaces.Message) string { return m.ID }
	}
	stack := clientstack.New(deps.Name).
		WithRetrier(deps.Retrier).
		WithCircuitBreaker(deps.CircuitBreaker).
		WithTimeout(deps.Timeout).
		WithTracer(deps.Tracer).
		WithMetrics(deps.Metrics).
		WithLogger(deps.Logger)

	return func(ctx context.Context, msg *interfaces.Message) error {
		// Dedup (outermost): a message already processed is acked without re-running.
		if deps.Dedup != nil {
			if seen, err := deps.Dedup.Seen(ctx, keyOf(msg)); err == nil && seen {
				return nil
			}
		}
		// Inner: resilience + observability via the shared client stack.
		_, err := clientstack.Run(
			ctx,
			stack,
			"handle",
			clientstack.RunOpts{Retryable: true},
			func(c context.Context) (struct{}, error) { return struct{}{}, inner(c, msg) },
		)
		if err == nil {
			return nil
		}
		// DeadLetter: after retries + CB exhaust, route the poison message; ack when it
		// lands, otherwise return the error so the source message is redriven.
		if deps.DeadLetter != nil && deps.DeadLetter.Send(ctx, interfaces.DeadLetter{
			ID:      msg.ID,
			Payload: msg.Payload,
			Reason:  err.Error(),
		}) {
			return nil
		}
		return err
	}
}
