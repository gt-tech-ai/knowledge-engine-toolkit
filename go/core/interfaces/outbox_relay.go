package interfaces

import "context"

// OutboxRelay is one outbox relay as its scheduler sees it: a single pass that moves due
// rows from the store to the sink. The workflows/outbox Relay implements it; a loop
// ticker, a leader-elected runner or a test drives it without knowing the lane, the
// partition or the sink behind it.
type OutboxRelay interface {
	// RunOnce runs one relay pass: claim a batch, deliver it, finalize every row. A
	// failed claim or finalize is returned coded; the caller decides when to try again.
	RunOnce(ctx context.Context) error
}
