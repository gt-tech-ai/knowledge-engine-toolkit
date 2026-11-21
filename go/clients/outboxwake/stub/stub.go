// Package stub is the zero-infrastructure OutboxWaker: it never wakes anything, so a
// relay boots with no database connection and runs on its poll alone.
package stub

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Waker never delivers a wake.
type Waker struct {
	// wakes is never written and never closed, so a relay selecting on it just keeps
	// waiting for its timer.
	wakes chan types.OutboxWake
}

// New returns the stub waker.
func New() *Waker { return &Waker{wakes: make(chan types.OutboxWake)} }

// Wakes returns a channel that never yields.
func (w *Waker) Wakes() <-chan types.OutboxWake { return w.wakes }

// Start does nothing and succeeds.
func (*Waker) Start(context.Context) error { return nil }

// Close does nothing and succeeds.
func (*Waker) Close() error { return nil }
