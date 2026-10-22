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

// Select returns the staged files matching spec.Glob (every staged file when empty),
// sorted.
func (s stagedFiles) Select(
	ctx context.Context,
	_ interfaces.Codebase,
	spec types.TargetSpec,
) ([]string, error) {
	var patterns []string
	if spec.Glob != "" {
		patterns = []string{spec.Glob}
	}
	out, err := s.git.StagedFiles(ctx, s.root, patterns)
	if err != nil {
		return nil, err
	}
	slices.Sort(out)
	return out, nil
}
