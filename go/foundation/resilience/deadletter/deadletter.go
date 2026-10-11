// Package deadletter provides the dead-letter-queue facade — the Go mirror of
// techai_webutils.foundation.resilience.dlq. It wraps a DeadLetterBackend with
// swallow-and-report semantics: a backend failure never propagates to crash the
// consumer loop, but Send returns false so the caller can leave the source message for
// redrive instead of deleting it (avoiding silent data loss on a DLQ outage).
package deadletter

import (
	"context"
	"sync"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Queue is a facade over a DeadLetterBackend that swallows and logs backend failures.
// Send returns true when the letter was durably recorded, false when the backend
// failed (already logged) — the caller uses that to decide delete-vs-redrive of the
// source message.
type Queue struct {
	// backend durably records dead letters.
	backend interfaces.DeadLetterBackend

	// logger records a backend failure once (nil disables the log).
	logger interfaces.Logger
}

// New builds a Queue over the given backend, logging backend failures via logger.
func New(backend interfaces.DeadLetterBackend, logger interfaces.Logger) *Queue {
	return &Queue{backend: backend, logger: logger}
}

// Send records the dead letter, returning true on success and false if the backend
// failed. A backend failure is logged at Error (a DLQ outage is a genuine data-loss
// risk, not an inner-seam retry) and never propagated, so the consumer loop survives.
func (q *Queue) Send(ctx context.Context, letter interfaces.DeadLetter) bool {
	if err := q.backend.Send(ctx, letter); err != nil {
		if q.logger != nil {
			q.logger.Error(
				"failed to record dead letter",
				"dead_letter_id", letter.ID,
				"reason", letter.Reason,
				"error", err.Error(),
			)
		}
		return false
	}
	return true
}

// StubBackend is an in-memory DeadLetterBackend that records letters for tests and
// local dev (the Go mirror of the Python StubDeadLetterBackend).
type StubBackend struct {
	// Letters holds every dead letter recorded via Send, in arrival order.
	Letters []interfaces.DeadLetter
	// mu guards Letters.
	mu sync.Mutex
}

// compile-time check: StubBackend satisfies the DeadLetterBackend contract.
var _ interfaces.DeadLetterBackend = (*StubBackend)(nil)

// Send records the letter in memory.
func (b *StubBackend) Send(_ context.Context, letter interfaces.DeadLetter) error {
	b.mu.Lock()
	b.Letters = append(b.Letters, letter)
	b.mu.Unlock()
	return nil
}
