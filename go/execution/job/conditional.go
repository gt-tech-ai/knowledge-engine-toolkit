package job

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Conditional wraps an AnyJob and runs it only when condition returns true.
// When the condition is false the job returns a single Skip result.
type Conditional struct {
	// inner is the wrapped job that runs when the condition holds.
	inner interfaces.AnyJob

	// condition is evaluated at execution time; the inner job runs only when it
	// returns true, otherwise the job reports a single Skip result.
	condition func(ctx context.Context, root string) bool
}

// Compile-time assertion that *Conditional satisfies interfaces.AnyJob.
var _ interfaces.AnyJob = (*Conditional)(nil)

// Meta implements interfaces.AnyJob.
func (c *Conditional) Meta() types.JobMeta { return c.inner.Meta() }

// Execute implements interfaces.AnyJob.
func (c *Conditional) Execute(
	ctx context.Context,
	runner interfaces.CommandRunner,
	root string,
) (types.StepResults, error) {
	if !c.condition(ctx, root) {
		return types.StepResults{
			{Name: c.inner.Meta().Name, Status: types.StatusSkip},
		}, nil
	}
	return c.inner.Execute(ctx, runner, root)
}

// When wraps job with a condition; the job runs only when condition returns true.
func When(
	job interfaces.AnyJob,
	condition func(ctx context.Context, root string) bool,
) *Conditional {
	return &Conditional{inner: job, condition: condition}
}
