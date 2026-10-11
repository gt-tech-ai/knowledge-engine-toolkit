package types

import (
	"time"

	"github.com/google/uuid"
)

// OutboxRouteAttribute is the record attribute a routing sink reads to pick the
// record's destination (e.g. one queue per event type).
const OutboxRouteAttribute = "route"

// OutboxRecord is one row of a transactional outbox: a message written in the
// same transaction as the business change it announces, later delivered to a
// sink by the outbox relay.
type OutboxRecord struct {
	// CreatedAt is when the row was written (the sink keys date partitions by it, in UTC).
	CreatedAt time.Time
	// Attributes carry sink-specific metadata (e.g. OutboxRouteAttribute for a
	// routing sink, "kms_key_id" for the S3 sink).
	Attributes map[string]string
	// Tenant is the owning tenant (organization) id.
	Tenant string
	// Lane names the outbox lane (one relay drains one lane).
	Lane string
	// Key is the record's ordering key: the SQS sink's FIFO message group id and
	// the S3 sink's {key} object-key placeholder ("" = none).
	Key string
	// Payload is the message body, delivered verbatim.
	Payload []byte
	// Attempts counts delivery attempts, including the one in progress after a claim.
	Attempts int
	// ID uniquely identifies the row.
	ID uuid.UUID
}

// OutboxStats is a point-in-time summary of one lane.
type OutboxStats struct {
	// OldestPending is the CreatedAt of the oldest undelivered, unparked row (zero when
	// none).
	OldestPending time.Time
	// Pending counts undelivered, unparked rows.
	Pending int
	// Parked counts rows that exhausted their attempts and await an operator.
	Parked int
}
