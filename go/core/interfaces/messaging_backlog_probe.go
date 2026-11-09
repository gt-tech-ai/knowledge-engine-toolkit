package interfaces

import (
	"context"
)

// BacklogProbe is a consumer capability reporting a topic's backlog. Alerts and
// autoscaling read the broker-neutral gauges messaging_backlog_messages{topic} and
// messaging_oldest_age_seconds{topic} it feeds, not a broker's own metrics. It mirrors
// the Python BacklogProbe; an opt-in capability per
// ARCHITECTURE.md#interface-composition.
type BacklogProbe interface {
	// Backlog returns the work currently waiting on topic.
	Backlog(ctx context.Context, topic string) (Backlog, error)
}

// MessageConsumer consumes messages from a queue/topic, acking or nacking each one
// by its handler's result.
type MessageConsumer interface {
	// Subscribe starts consuming messages from the specified topic.
	// The handler is called for each message. Return nil to ack, error to nack.
	Subscribe(ctx context.Context, topic string, handler MessageHandler) error

	// Close stops the consumer gracefully.
	Close() error
}

// DynamicConsumer is a MessageConsumer that can also remove a per-channel subscription at
// runtime. Pub/Sub brokers (e.g. Redis) multiplex many channels over one subscription and
// add/remove them dynamically; SQS-style queue consumers do not. It is an opt-in
// capability interface that composes with MessageConsumer
// (ARCHITECTURE.md#interface-composition) rather than widening the shared contract —
// backends that cannot unsubscribe simply do not implement it.
type DynamicConsumer interface {
	// MessageConsumer provides the base Subscribe/Close operations.
	MessageConsumer

	// Unsubscribe stops delivering messages from topic. It is a no-op if topic
	// was never subscribed; a broker error is returned.
	Unsubscribe(ctx context.Context, topic string) error
}

// PatternConsumer is a DynamicConsumer that also supports glob-pattern (wildcard)
// subscriptions — the Redis PSUBSCRIBE model, where one subscription matches every
// channel whose name matches a pattern. It composes with DynamicConsumer (and thus
// MessageConsumer) per ARCHITECTURE.md#interface-composition; backends without pattern
// matching do not implement it.
type PatternConsumer interface {
	// DynamicConsumer provides the base consume operations plus per-channel Unsubscribe.
	DynamicConsumer

	// PSubscribe starts delivering messages from every channel matching pattern
	// (glob syntax) to handler. Runtime add.
	PSubscribe(ctx context.Context, pattern string, handler MessageHandler) error

	// PUnsubscribe stops delivering messages for pattern. It is a no-op if pattern
	// was never subscribed; a broker error is returned. Runtime remove.
	PUnsubscribe(ctx context.Context, pattern string) error
}

// MessageHandler processes a single message.
type MessageHandler func(ctx context.Context, msg *Message) error
