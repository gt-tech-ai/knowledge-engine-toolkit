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
// at-least-once consumer accepts.
func Decorate(
	base interfaces.MessagePublisher,
	name string,
	deps decorate.Deps,
) interfaces.MessagePublisher {
	return &decorated{base: base, Base: client.NewProducerBase(name, deps)}
}

// decorated runs each MessagePublisher operation through the client tier.
type decorated struct {
	// base is the wrapped publisher.
	base interfaces.MessagePublisher

	// Base runs the operations.
	client.Base
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
