package memory

import (
	"context"
	"fmt"
	"sync/atomic"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/tracer"
)

// Compile-time interface assertion.
var _ interfaces.MessagePublisher = (*Publisher)(nil)

// Publisher enqueues messages onto a Broker instead of SQS (dev/test; no external
// broker).
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
	msg := &interfaces.Message{
		ID:       fmt.Sprintf("mem-%d", p.seq.Add(1)),
		Topic:    topic,
		Payload:  payload,
		Metadata: map[string]string{},
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

// PublishBatch enqueues each payload on topic in order.
func (p *Publisher) PublishBatch(
	ctx context.Context,
	topic string,
	payloads [][]byte,
) error {
	for _, payload := range payloads {
		if err := p.Publish(ctx, topic, payload); err != nil {
			return err
		}
	}
	return nil
}
