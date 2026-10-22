package scaffold

import (
	"context"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Apply writes plan's FileCreate and FileUpdate entries through write, or nothing
// under dryRun. A plan with a FileConflict writes nothing and is CodeConflict, naming
// each conflicting file. Applying a plan made after a previous Apply writes nothing.
func Apply(
	ctx context.Context,
	write func(path string, data []byte) error,
	plan types.FilePlan,
	dryRun bool,
) error {
	var conflicts []string
	for _, e := range plan.Entries {
		if e.Action == types.FileConflict {
			conflicts = append(conflicts, e.Path+": "+e.Reason)
		}
	}
	if len(conflicts) > 0 {
		return apperr.New(
			apperr.CodeConflict,
			"scaffold conflicts: "+strings.Join(conflicts, "; "),
		)
	}
	if dryRun {
		return nil
	}
	for _, e := range plan.Entries {
		if e.Action != types.FileCreate && e.Action != types.FileUpdate {
			continue
		}
		if err := ctx.Err(); err != nil {
			return apperr.Wrap(err, apperr.ContextCode(err, apperr.CodeCanceled), "apply")
		}
		if err := write(e.Path, e.Content); err != nil {
			return err
		}
	}
	return nil
}
