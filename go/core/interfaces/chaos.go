package interfaces

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// FaultInjector injects and reverts a chaos fault; every clients/chaos Kind implements
// it.
type FaultInjector interface {
	// Inject starts the fault. It may have taken effect even when it returns an error,
	// so the caller reverts after every attempt. A fault whose resource already exists
	// belongs to another run and is CodeConflict.
	Inject(ctx context.Context, f types.Fault) error

	// Revert removes the fault and waits for the backend to restore the target. It is
	// idempotent: reverting a fault that is not (or no longer) injected succeeds.
	Revert(ctx context.Context, f types.Fault) error

	// Active lists the faults this injector's owner injected that are still live in
	// namespace (any run's), so a run can refuse to overlap another.
	Active(ctx context.Context, namespace string) ([]string, error)
}
