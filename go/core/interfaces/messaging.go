package interfaces

import (
	"context"
)

// MessagePublisher publishes messages (typically domain events) to a message broker
// (SQS, Watermill, etc.).
type MessagePublisher interface {
	// Publish sends a message to the specified topic.
	Publish(ctx context.Context, topic string, payload []byte) error

	// PublishBatch sends multiple messages to the specified topic.
	PublishBatch(ctx context.Context, topic string, payloads [][]byte) error
}

// CountingPublisher is a MessagePublisher that also reports how many subscribers received
// a published message — the Redis PUBLISH model, whose reply is the number of clients the
// message was delivered to. It is an opt-in capability interface that composes with
// MessagePublisher (ARCHITECTURE.md#interface-composition) rather than widening the
// shared contract: a fan-out consumer that must know whether a message reached anyone
// (e.g. the WebSocket backplane deciding cluster-wide delivery) depends on it, while a
// broker with no delivered-count notion (SQS, an in-memory bus) simply does not implement
// it.
type CountingPublisher interface {
	// MessagePublisher provides the base Publish/PublishBatch operations.
	MessagePublisher

	// PublishCount sends payload to topic and returns the number of subscribers that
	// received it (0 when no subscriber is listening). For Redis Pub/Sub this is the
	// PUBLISH reply; a positive count means the message was delivered to at least one
	// subscriber.
	PublishCount(ctx context.Context, topic string, payload []byte) (int, error)
}

// PartitionKeyAttribute is the message attribute (SQS) or metadata key (memory) a
// KeyedPublisher carries a message's key in, so a consumer can read it back.
const PartitionKeyAttribute = "partition_key"

// KeyedPayload is one message of a keyed batch: its partition key and body.
type KeyedPayload struct {
	// Key groups related messages (e.g. one document's events); a broker that orders
	// per key delivers messages with the same key in publish order.
	Key string

	// Payload is the raw message body.
	Payload []byte
}

// KeyedPublisher is a MessagePublisher that also attaches a partition key to each
// message. It is an opt-in capability interface (ARCHITECTURE.md#interface-composition):
// a broker that partitions by key (Kafka, Kinesis, SQS FIFO groups) orders messages
// per key, while SQS standard queues only carry the key as the partition_key attribute
// so a consumer can still read it. Redis Pub/Sub carries no attributes and does not
// implement it.
type KeyedPublisher interface {
	// MessagePublisher provides the unkeyed Publish/PublishBatch operations.
	MessagePublisher

	// PublishKeyed sends payload to topic under key.
	PublishKeyed(ctx context.Context, topic, key string, payload []byte) error

	// PublishBatchKeyed sends each item to topic under its own key, in order.
	PublishBatchKeyed(ctx context.Context, topic string, items []KeyedPayload) error

	// OrderedPerKey reports whether the broker delivers messages that share a key in
	// publish order. A consumer that needs per-key order checks it at startup rather
	// than assuming it.
	OrderedPerKey() bool
}

// Backlog is the work waiting on one topic.
type Backlog struct {
	// OldestAgeSeconds is the age of the oldest waiting message, or nil when the broker
	// cannot report it cheaply (SQS keeps it only as a CloudWatch metric).
	OldestAgeSeconds *float64

	// Messages is how many messages wait to be received (SQS:
	// ApproximateNumberOfMessages; Kafka: consumer-group lag).
	Messages int64
}

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

// Message represents a message received from a queue.
type Message struct {
	// Metadata holds optional key-value pairs attached to the message.
	Metadata map[string]string `json:"metadata,omitempty"`

	// ID is the unique identifier of the message.
	ID string `json:"id"`

	// Topic is the queue or topic the message was received from.
	Topic string `json:"topic"`

	// Payload is the raw message body.
	Payload []byte `json:"payload"`

	// Timestamp is the Unix timestamp when the message was produced.
	Timestamp int64 `json:"timestamp"`
}
