package decorators

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// HandlerObservability decorates a message handler with per-message logging and
// metrics. Its metric instruments are created ONCE (at construction), so a
// subscriber that calls Wrap on every Subscribe reuses them rather than
// re-registering the same collectors. A nil logger or metrics disables that layer.
//
// Only per-message concerns belong here (the handler's own success/failure and
// latency); a consumer's infrastructure resilience — receive-loop backoff, queue
// resolution, ack retries — is intrinsic to a reliable consumer and stays in the
// backend core, not in a handler decorator.
type HandlerObservability struct {
	// logger logs a handler error (a nacked message); nil disables the logging layer.
	logger interfaces.Logger
	// total counts every message handled, labeled by topic.
	total interfaces.Counter
	// failures counts handles that returned an error, labeled by topic.
	failures interfaces.Counter
	// latency records per-handle duration in seconds, labeled by topic.
	latency interfaces.Histogram
}

// NewHandlerObservability builds the handler decorators from logger + metrics,
// creating the metric instruments once. Either may be nil to disable that layer.
func NewHandlerObservability(
	logger interfaces.Logger, metrics interfaces.Metrics,
) *HandlerObservability {
	o := &HandlerObservability{logger: logger}
	if metrics != nil {
		o.total = metrics.Counter(
			"messaging_handle_total", "Total messages handled", "topic",
		)
		o.failures = metrics.Counter(
			"messaging_handle_failures_total", "Failed message handles", "topic",
		)
		o.latency = metrics.Histogram(
			"messaging_handle_duration_seconds",
			"Message handle latency in seconds",
			[]float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
			"topic",
		)
	}
	return o
}

// Wrap returns handler decorated with the configured observability: metrics
// innermost (timing the raw handler), logging outermost.
func (o *HandlerObservability) Wrap(
	handler interfaces.MessageHandler,
) interfaces.MessageHandler {
	h := handler
	if o.total != nil {
		h = o.withMetrics(h)
	}
	if o.logger != nil {
		h = o.withLogging(h)
	}
	return h
}

// withLogging logs a handler error (a nacked message), tagged with topic + id.
func (o *HandlerObservability) withLogging(
	next interfaces.MessageHandler,
) interfaces.MessageHandler {
	return func(ctx context.Context, msg *interfaces.Message) error {
		err := next(ctx, msg)
		if err != nil {
			o.logger.WithContext(ctx).Error(
				"message handle failed",
				"topic", msg.Topic, "message_id", msg.ID, "error", err,
			)
		}
		return err
	}
}

// withMetrics records a count, a failure count, and a latency histogram per handle.
func (o *HandlerObservability) withMetrics(
	next interfaces.MessageHandler,
) interfaces.MessageHandler {
	return func(ctx context.Context, msg *interfaces.Message) error {
		start := time.Now()
		err := next(ctx, msg)
		o.latency.Observe(time.Since(start).Seconds(), msg.Topic)
		o.total.Inc(msg.Topic)
		if err != nil {
			o.failures.Inc(msg.Topic)
		}
		return err
	}
}
