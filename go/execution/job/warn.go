package job

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Warn makes j advisory: it keeps j's metadata and downgrades each Fail step j reports
// to Warn, so j never fails its gate. An execution error (j could not run) is returned
// unchanged.
func Warn(j interfaces.AnyJob) interfaces.AnyJob {
	return decorated{
		meta: j.Meta(),
		run: func(
			ctx context.Context,
			runner interfaces.CommandRunner,
			root string,
		) (types.StepResults, error) {
			results, err := j.Execute(ctx, runner, root)
			for i := range results {
				if results[i].Status == types.StatusFail {
					results[i].Status = types.StatusWarn
				}
			}
			return results, err
		},
	}
}
