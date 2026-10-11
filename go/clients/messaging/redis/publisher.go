package redis

import (
	"context"

	goredis "github.com/redis/go-redis/v9"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/lifecycle"
)

// Compile-time interface assertions: the Redis publisher satisfies the base
// MessagePublisher and the CountingPublisher capability (PUBLISH reports a
// subscriber count).
var (
	// Publisher satisfies the base MessagePublisher contract.
	_ interfaces.MessagePublisher = (*Publisher)(nil)
	// Publisher also satisfies CountingPublisher (PUBLISH reports a subscriber count).
	_ interfaces.CountingPublisher = (*Publisher)(nil)
)

// Publisher publishes messages to Redis channels via PUBLISH (fire-and-forget,
// at-most-once — Redis drops a message that has no live subscriber). It holds the
// shared go-redis client and opens no connection of its own.
type Publisher struct {
	// NoOp supplies the no-op Start/Stop: PUBLISH is a stateless command on the
	// shared client, so the publisher owns no lifecycle resource.
	lifecycle.NoOp

	// client is the shared go-redis client (injected via Config.Client).
	client *goredis.Client
}

// NewPublisher creates a Redis Pub/Sub publisher over the injected client. It
// returns a coded InvalidInput error if the client is nil — the publisher never
// dials its own connection.
func NewPublisher(cfg Config) (*Publisher, error) {
	if cfg.Client == nil {
		return nil, apperr.New(apperr.CodeInvalidInput, "redis messaging: nil client")
	}
	return &Publisher{client: cfg.Client}, nil
}

// Publish sends payload to the channel named topic via PUBLISH. Delivery is
// at-most-once: a message with no live subscriber is dropped by Redis.
func (p *Publisher) Publish(ctx context.Context, topic string, payload []byte) error {
	if err := p.client.Publish(ctx, topic, payload).Err(); err != nil {
		return apperr.Wrap(err, apperr.CodeUnavailable, "redis publish")
	}
	return nil
}

// PublishCount sends payload to topic via PUBLISH and returns the number of
// subscribers that received it — the PUBLISH reply. A count of 0 means no subscriber
// was listening (the message was dropped, at-most-once). A caller fanning out to
// per-recipient channels uses it to tell whether any replica currently has a
// subscriber for that recipient.
func (p *Publisher) PublishCount(
	ctx context.Context,
	topic string,
	payload []byte,
) (int, error) {
	n, err := p.client.Publish(ctx, topic, payload).Result()
	if err != nil {
		return 0, apperr.Wrap(err, apperr.CodeUnavailable, "redis publish")
	}
	return int(n), nil
}

// PublishBatch sends each payload to topic in one pipelined round-trip (N PUBLISH
// commands, one network flush) rather than N separate calls.
func (p *Publisher) PublishBatch(
	ctx context.Context,
	topic string,
	payloads [][]byte,
) error {
	if len(payloads) == 0 {
		return nil
	}
	pipe := p.client.Pipeline()
	for _, payload := range payloads {
		pipe.Publish(ctx, topic, payload)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return apperr.Wrap(err, apperr.CodeUnavailable, "redis publish batch")
	}
	return nil
}
