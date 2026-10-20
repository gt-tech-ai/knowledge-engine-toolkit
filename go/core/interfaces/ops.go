package interfaces

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Operation is one operation every front-end (a CLI, an MCP server, a scheduled job)
// runs the same way: describe it, decode its input, run it.
//
// §2.3 exemption — SDK seam: the contract between the operations registry and the
// front-end adapters, with no core interface to embed; the registry's decorators and
// the transports depend on it and tests inject a mock.
type Operation interface {
	// Descriptor returns the operation's descriptor.
	Descriptor() types.OperationDescriptor
	// NewInput returns a pointer to a fresh, zero-valued input the front-end decodes
	// into.
	NewInput() any
	// InputSchema returns the JSON Schema of the input NewInput returns.
	InputSchema() []byte
	// Run executes the operation with a decoded input, reporting progress to obs.
	Run(ctx context.Context, input any, obs ExecutionObserver) error
}

// Reservation holds the operations registry's working-tree slot for one run of one
// operation, so a front-end can claim the slot synchronously and run the operation
// later (a background run). It is one-shot: Run releases the slot when the operation
// returns.
//
// §2.3 exemption — SDK seam: the contract between the operations registry and the
// background-run table, with no core interface to embed.
type Reservation interface {
	// Run runs the reserved operation with a decoded input, reporting progress to obs,
	// then releases the slot; a second call, or a call after Release, fails with
	// CodeInternal.
	Run(ctx context.Context, input any, obs ExecutionObserver) error
	// Release frees the slot without running; it is a no-op once Run or Release ran.
	Release()
}
