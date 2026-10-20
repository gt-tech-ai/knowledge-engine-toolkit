package selectors

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// pythonProjects is KindPythonProjects.
type pythonProjects struct{}

// Select returns every directory that holds a pyproject.toml.
func (pythonProjects) Select(
	ctx context.Context,
	cb interfaces.Codebase,
	_ types.TargetSpec,
) ([]string, error) {
	found, err := cb.Files(ctx, types.FileSelector{Globs: []string{"**/pyproject.toml"}})
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(found))
	for _, f := range found {
		paths = append(paths, f.Path)
	}
	return dirsOf(paths), nil
}
