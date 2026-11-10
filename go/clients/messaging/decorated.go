package messaging

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Decorate runs every publish of base through the client tier as the client named
// name, each as the operation named by its topic in a producer span. A publish is
// retried only when deps carries a Retrier: a re-sent message is a duplicate an
// at-least-once consumer accepts. The topic is the operation, so the config tunes the
// publisher as a whole; Kinds passes client.Producer in opts for the producer spans.
// When base is an interfaces.KeyedPublisher the result is one too, so decoration never
// hides the keyed capability.
func Decorate(
	base interfaces.MessagePublisher,
	decs decorate.Decorations,
	name string,
	opts ...client.Option,
) (interfaces.MessagePublisher, error) {
	b, err := client.NewWireBase(decs, name, opts...)
	if err != nil {
		return nil, err
	}

	d := &decorated{base: base, Base: &b}
	if keyed, ok := base.(interfaces.KeyedPublisher); ok {
		return &decoratedKeyed{decorated: d, keyed: keyed}, nil
	}

	return d, nil
}

// decorated runs each MessagePublisher operation through the client tier.
type decorated struct {
	// Base runs the operations.
	*client.Base

	// base is the wrapped publisher.
	base interfaces.MessagePublisher
}

var _ interfaces.MessagePublisher = (*decorated)(nil)

// Publish sends payload to topic.
func (d *decorated) Publish(ctx context.Context, topic string, payload []byte) error {
	return d.Do(ctx, topic, func(ctx context.Context) error {
		return d.base.Publish(ctx, topic, payload)
	})
}

// PublishBatch sends payloads to topic.
func (d *decorated) PublishBatch(
	ctx context.Context,
	topic string,
	payloads [][]byte,
) error {
	return d.Do(ctx, topic, func(ctx context.Context) error {
		return d.base.PublishBatch(ctx, topic, payloads)
	})
}

// decoratedKeyed is decorated for a KeyedPublisher base: the keyed operations run
// through the same client tier.
type decoratedKeyed struct {
	*decorated

	// keyed is the wrapped publisher, viewed through its keyed capability.
	keyed interfaces.KeyedPublisher
}

var _ interfaces.KeyedPublisher = (*decoratedKeyed)(nil)

// PublishKeyed sends payload to topic under key.
func (d *decoratedKeyed) PublishKeyed(
	ctx context.Context,
	topic, key string,
	payload []byte,
) error {
	return d.Do(ctx, topic, func(ctx context.Context) error {
		return d.keyed.PublishKeyed(ctx, topic, key, payload)
	})
}

// PublishBatchKeyed sends items to topic, each under its key.
func (d *decoratedKeyed) PublishBatchKeyed(
	ctx context.Context,
	topic string,
	items []interfaces.KeyedPayload,
) error {
	return d.Do(ctx, topic, func(ctx context.Context) error {
		return d.keyed.PublishBatchKeyed(ctx, topic, items)
	})
}

// OrderedPerKey reports the wrapped publisher's ordering.
func (d *decoratedKeyed) OrderedPerKey() bool { return d.keyed.OrderedPerKey() }
