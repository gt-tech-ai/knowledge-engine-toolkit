package interfaces

import "context"

// DeadLetter is a message that could not be processed and is routed to a
// dead-letter sink for later inspection or redrive.
type DeadLetter struct {
	// ID identifies the dead-lettered message (typically the source message id).
	ID string
	// Reason records why processing failed and the message was dead-lettered.
	Reason string
	// Payload is the original message body, preserved for inspection or redrive.
	Payload []byte
}

// DeadLetterBackend durably records dead letters (e.g. an SQS dead-letter queue).
//
// Implementations: SQS (production), an in-memory stub (tests/local). All handler
// code depends on this interface, never on a concrete backend — swap via a
// deadletter factory.
type DeadLetterBackend interface {
	// Send durably records the dead letter, returning an error if the sink is
	// unavailable so the caller can decide delete-vs-redrive of the source message.
	Send(ctx context.Context, letter DeadLetter) error
}
