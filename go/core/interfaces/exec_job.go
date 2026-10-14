package interfaces

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Named is the identity half of an execution unit: its Meta for display and tool
// resolution. Both command jobs (AnyJob) and runner-less units (Runnable) compose
// it, so the gate can label either without knowing how it executes.
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

// Runnable is a runner-less execution unit: in-process work (e.g. DB seeding) that
// needs neither a CommandRunner nor a filesystem root. Compose it into a gate with
// the execution adapter (job.Base.Runnable), which lifts it to AnyJob — so callers build
// typed Runnables instead of passing a nil CommandRunner through the API.
type Runnable interface {
	// Named supplies the unit's identity (Meta) for display and tool resolution.
	Named

	// Run performs the unit's in-process work and returns collected step results.
	Run(ctx context.Context) (types.StepResults, error)
}
