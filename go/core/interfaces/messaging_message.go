package interfaces

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
