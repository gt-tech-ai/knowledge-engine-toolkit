package selectors

import (
	"context"
	"slices"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// stagedFiles is KindStagedFiles.
type stagedFiles struct {
	// git lists the staged files.
	git interfaces.GitOps

	// root is the working tree git is asked about.
	root string
}

// Select returns the staged files matching spec.Glob or spec.Globs (every staged file
// when both are empty),
// sorted.
func (s stagedFiles) Select(
	ctx context.Context,
	_ interfaces.Codebase,
	spec types.TargetSpec,
) ([]string, error) {
	out, err := s.git.StagedFiles(ctx, s.root, globs(spec))
	if err != nil {
		return nil, err
	}
	slices.Sort(out)
	return out, nil
}
