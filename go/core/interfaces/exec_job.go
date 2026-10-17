package interfaces

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Named is the identity half of an execution unit: its Meta for display and tool
// resolution. AnyJob composes it, so the gate can label a job without knowing how it
// executes.
type Named interface {
	// Meta returns the unit's identity for display and tool resolution.
	Meta() types.JobMeta
}

// AnyJob is a command-oriented execution unit: it runs against a CommandRunner
// rooted at a filesystem path. The generic Job[T] in the execution module and the
// CLI's command jobs implement it; the engine and gate operate on this type.
type AnyJob interface {
	// Named supplies the job's identity (Meta) for display and tool resolution.
	Named

	// Execute runs the job against runner (rooted at root) and returns collected
	// step results.
	Execute(
		ctx context.Context,
		runner CommandRunner,
		root string,
	) (types.StepResults, error)
}
