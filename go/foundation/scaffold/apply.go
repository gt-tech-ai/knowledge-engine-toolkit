package scaffold

import (
	"context"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// writeEntry writes one entry if the plan creates or updates it, stopping when ctx ended.
func writeEntry(
	ctx context.Context,
	write func(path string, data []byte) error,
	fileEntry *types.FileEntry,
) error {
	if fileEntry.Action != types.FileCreate && fileEntry.Action != types.FileUpdate {
		return nil
	}

	err := ctx.Err()
	if err != nil {
		return apperr.Wrap(err, apperr.ContextCode(err, apperr.CodeCanceled), "apply")
	}

	return write(fileEntry.Path, fileEntry.Content)
}

// Apply writes plan's FileCreate and FileUpdate entries through write.
// A plan with a FileConflict writes nothing and is CodeConflict, naming
// each conflicting file. Applying a plan made after a previous Apply writes nothing.
func Apply(
	ctx context.Context,
	write func(path string, data []byte) error,
	plan types.FilePlan,
) error {
	err := Conflicts(plan)
	if err != nil {
		return err
	}

	for i := range plan.Entries {
		err := writeEntry(ctx, write, &plan.Entries[i])
		if err != nil {
			return err
		}
	}

	return nil
}

// Conflicts names every conflicting entry as CodeConflict, or is nil when the plan has
// none. It writes nothing, so it is the dry run of Apply.
func Conflicts(plan types.FilePlan) error {
	var conflicts []string

	for _, e := range plan.Entries {
		if e.Action == types.FileConflict {
			conflicts = append(conflicts, e.Path+": "+e.Reason)
		}
	}

	if len(conflicts) == 0 {
		return nil
	}

	return apperr.New(
		apperr.CodeConflict,
		"scaffold conflicts: "+strings.Join(conflicts, "; "),
	)
}
