package memory

import (
	"context"
	"sync"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Compile-time interface assertion.
var _ interfaces.MessageConsumer = (*Subscriber)(nil)

// Subscriber consumes messages from a Broker instead of SQS (dev/test; no external
// broker).
type Subscriber struct {
	// broker is the in-process broker messages are consumed from.
	broker *Broker
	// done is closed by Close to stop the receive loop.
	done chan struct{}
	// once ensures done is closed at most once, making Close idempotent.
	once sync.Once
}

// NewSubscriber consumes from broker; a Publisher built from the same broker feeds this
// subscriber.
func NewSubscriber(broker *Broker) *Subscriber {
	return &Subscriber{broker: broker, done: make(chan struct{})}
}

// Subscribe delivers each message on topic to handler until Close is called or ctx is
// cancelled. A handler error is dropped: the in-memory broker has no redelivery, so this
// is a stub's best effort — good enough to exercise the consume path with no SQS, not to
// model at-least-once.
func (s *Subscriber) Subscribe(
	ctx context.Context,
	topic string,
	handler interfaces.MessageHandler,
) error {
	ch := s.broker.topic(topic)
	for {
		select {
		case msg := <-ch:
			_ = handler(ctx, msg)
		case <-ctx.Done():
			return apperr.Wrap(
				ctx.Err(),
				apperr.ContextCode(ctx.Err(), apperr.CodeCanceled),
				"subscription canceled",
			)
		case <-s.done:
			return nil
		}
	}
}

// Close stops the receive loop (idempotent).
func (s *Subscriber) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}
