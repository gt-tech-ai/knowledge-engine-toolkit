package job

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// runnableJob adapts a runner-less interfaces.Runnable to interfaces.AnyJob so it
// can be composed into a gate alongside command jobs. Execute discards the
// CommandRunner and root: a Runnable performs in-process work (e.g. DB seeding)
// and needs neither — the discard is the single, typed seam between the two
// shapes, replacing per-consumer hand-rolled adapters that passed a nil runner.
type runnableJob struct {
	// r is the wrapped runner-less unit; its Run supplies the step results.
	r interfaces.Runnable
}

// Meta delegates to the wrapped Runnable's identity.
func (j runnableJob) Meta() types.JobMeta { return j.r.Meta() }

// Execute runs the wrapped Runnable, ignoring the CommandRunner and root that
// command jobs use.
func (j runnableJob) Execute(
	ctx context.Context,
	_ interfaces.CommandRunner,
	_ string,
) (types.StepResults, error) {
	return j.r.Run(ctx)
}

// Adapt lifts a runner-less Runnable into an AnyJob for gate composition, so
// callers compose typed Runnables instead of threading a nil CommandRunner.
func Adapt(r interfaces.Runnable) interfaces.AnyJob { return runnableJob{r: r} }

// Compile-time assertion that runnableJob satisfies interfaces.AnyJob.
var _ interfaces.AnyJob = runnableJob{}
