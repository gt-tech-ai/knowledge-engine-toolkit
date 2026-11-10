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
