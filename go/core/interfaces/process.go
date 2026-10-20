package interfaces

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// ProcessHandle is a started process.
type ProcessHandle interface {
	// Wait blocks until the process exits and returns its exit error; every caller gets
	// the same result.
	Wait() error

	// Stop asks the process to exit, kills it when ctx ends first, and waits for it.
	Stop(ctx context.Context) error
}

// Supervisor starts processes, waits for them to be ready, and stops them.
type Supervisor interface {
	// Start starts spec and polls ready against it until a verdict passes; the process
	// is stopped and the error is TIMEOUT when ctx ends first, or UPSTREAM when the
	// process exits first. A nil ready returns as soon as the process starts.
	Start(
		ctx context.Context,
		spec types.ProcessSpec,
		ready Probe[ProcessHandle],
	) (ProcessHandle, error)

	// Detach starts spec without supervising it, so it outlives the caller (on platforms
	// that support new sessions).
	Detach(spec types.ProcessSpec) error

	// StopAll stops every process Start started and that is still running, newest
	// first.
	StopAll(ctx context.Context) error
}
