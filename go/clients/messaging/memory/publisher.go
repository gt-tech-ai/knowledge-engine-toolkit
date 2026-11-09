package memory

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/tracer"
)

// Compile-time interface assertion.
var _ interfaces.KeyedPublisher = (*Publisher)(nil)

// Publisher enqueues messages onto a Broker instead of SQS (dev/test; no external
// broker). A keyed publish carries its key in the message metadata under
// interfaces.PartitionKeyAttribute; a topic is one FIFO channel delivered serially, so
// messages are delivered in publish order, per key included.
type Publisher struct {
	// broker is the in-process broker messages are enqueued onto.
	broker *Broker
	// seq is a monotonic counter used to synthesize unique message IDs.
	seq atomic.Int64
}

// NewPublisher publishes onto broker; a Subscriber built from the same broker receives
// the messages.
func NewPublisher(broker *Broker) *Publisher {
	return &Publisher{broker: broker}
}

// Publish enqueues payload on topic as a Message, or returns ctx's error if it is
// cancelled first.
func (p *Publisher) Publish(ctx context.Context, topic string, payload []byte) error {
	return p.enqueue(ctx, topic, map[string]string{}, payload)
}

// PublishKeyed enqueues payload on topic with key in its metadata.
func (p *Publisher) PublishKeyed(
	ctx context.Context,
	topic, key string,
	payload []byte,
) error {
	return p.enqueue(
		ctx,
		topic,
		map[string]string{interfaces.PartitionKeyAttribute: key},
		payload,
	)
}

// PublishBatchKeyed enqueues each item on topic under its key, in order.
func (p *Publisher) PublishBatchKeyed(
	ctx context.Context,
	topic string,
	items []interfaces.KeyedPayload,
) error {
	for _, item := range items {
		err := p.PublishKeyed(ctx, topic, item.Key, item.Payload)
		if err != nil {
			return err
		}
	}

	return nil
}

// OrderedPerKey is true: a topic's messages are delivered serially in publish order.
func (*Publisher) OrderedPerKey() bool { return true }

// PublishBatch enqueues each payload on topic in order.
func (p *Publisher) PublishBatch(
	ctx context.Context,
	topic string,
	payloads [][]byte,
) error {
	for _, payload := range payloads {
		err := p.Publish(ctx, topic, payload)
		if err != nil {
			return err
		}
	}

	return nil
}

// enqueue puts payload on topic as a Message with metadata plus ctx's trace context,
// or returns ctx's error if it is cancelled first.
func (p *Publisher) enqueue(
	ctx context.Context,
	topic string,
	metadata map[string]string,
	payload []byte,
) error {
	msg := &interfaces.Message{
		ID:       fmt.Sprintf("mem-%d", p.seq.Add(1)),
		Topic:    topic,
		Payload:  payload,
		Metadata: metadata,
	}
	tracer.Inject(ctx, msg.Metadata)

	select {
	case p.broker.topic(topic) <- msg:
		return nil
	case <-ctx.Done():
		return apperr.Wrap(
			ctx.Err(),
			apperr.ContextCode(ctx.Err(), apperr.CodeCanceled),
			"publish canceled",
		)
	}
}
