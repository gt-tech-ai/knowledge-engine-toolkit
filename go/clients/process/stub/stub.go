// Package stub is the Supervisor that starts nothing: every process is ready at once
// and exits cleanly when stopped.
package stub

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Supervisor is the stub Supervisor.
type Supervisor struct{}

// Compile-time assertion that Supervisor satisfies the contract.
var _ interfaces.Supervisor = Supervisor{}

// Start returns a handle without starting anything.
func (Supervisor) Start(
	context.Context,
	types.ProcessSpec,
	interfaces.Probe[interfaces.ProcessHandle],
) (interfaces.ProcessHandle, error) {
	return handle{}, nil
}

// Detach does nothing.
func (Supervisor) Detach(types.ProcessSpec) error { return nil }

// StopAll does nothing.
func (Supervisor) StopAll(context.Context) error { return nil }

// handle is a process that has already exited cleanly.
type handle struct{}

// Wait returns at once.
func (handle) Wait() error { return nil }

// Stop does nothing.
func (handle) Stop(context.Context) error { return nil }
