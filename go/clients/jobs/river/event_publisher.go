package river

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/events"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Compile-time interface assertion.
var _ interfaces.EventPublisher = (*EventPublisher)(nil)

// EventPublisher is a no-op EventPublisher: it accepts events without publishing
// them (a transactional outbox through River is not implemented).
type EventPublisher struct {
	// enqueuer is the job enqueuer used for transactional event publishing.
	enqueuer interfaces.JobEnqueuer
}

// NewEventPublisher creates a new event publisher.
func NewEventPublisher(enqueuer interfaces.JobEnqueuer) *EventPublisher {
	return &EventPublisher{
		enqueuer: enqueuer,
	}
}

// Publish accepts event without publishing it and returns nil.
func (p *EventPublisher) Publish(_ context.Context, _ events.Event) error {
	return nil
}

// PublishBatch calls Publish for each event, stopping at the first error.
func (p *EventPublisher) PublishBatch(ctx context.Context, evts []events.Event) error {
	for _, event := range evts {
		if err := p.Publish(ctx, event); err != nil {
			return err
		}
	}
	return nil
}
