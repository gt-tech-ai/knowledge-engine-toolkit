package job

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Warn makes j advisory: it keeps j's metadata and downgrades each Fail step j reports
// to Warn, so j never fails its gate. An execution error (j could not run) is returned
// unchanged.
func Warn(anyJob interfaces.AnyJob) interfaces.AnyJob {
	return &decorated{
		meta: anyJob.Meta(),
		run: func(
			ctx context.Context,
			runner interfaces.CommandRunner,
			root string,
		) (types.StepResults, error) {
			results, err := anyJob.Execute(ctx, runner, root)
			for i := range results {
				if results[i].Status == types.StatusFail {
					results[i].Status = types.StatusWarn
				}
			}

			return results, err
		},
		op: decorate.Subject{},
	}
}
