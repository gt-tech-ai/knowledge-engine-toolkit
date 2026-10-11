// Package events is the generic event contract: the Event interface, the metadata every
// event carries, and a Registry a consumer fills with its own event catalog to parse
// payloads.
package events

import "time"

// EventType is an event's type discriminator (e.g. "widget.created"); the consumer
// defines its own values.
type EventType string

// Event is the interface all events implement.
type Event interface {
	// Type returns the event's type discriminator.
	Type() EventType

	// Metadata returns the common metadata shared by all events.
	Metadata() EventMetadata
}

// EventMetadata holds common fields for all events.
type EventMetadata struct {
	// EventID is the unique identifier of the event.
	EventID string `json:"event_id"`

	// EventType is the type discriminator for the event.
	EventType EventType `json:"event_type"`

	// OccurredAt is the timestamp when the event occurred.
	OccurredAt time.Time `json:"occurred_at"`

	// TenantID is the tenant the event belongs to.
	TenantID string `json:"tenant_id"`

	// UserID is the user who triggered the event, if applicable.
	UserID string `json:"user_id,omitempty"`

	// CorrelationID links related events across services.
	CorrelationID string `json:"correlation_id,omitempty"`

	// TraceParent carries the W3C trace context (traceparent header) captured when
	// the event was produced, so a consumer can continue the originating trace —
	// e.g. request → outbox → relay publish → consumer — as one distributed
	// trace. Empty when no active trace context was present.
	TraceParent string `json:"traceparent,omitempty"`
}
