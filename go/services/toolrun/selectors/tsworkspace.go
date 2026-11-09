package selectors

import (
	"context"
	"path"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// workspaceFile is pnpm's workspace manifest at the codebase root.
const workspaceFile = "pnpm-workspace.yaml"

// tsWorkspace is KindTSWorkspace.
type tsWorkspace struct{}

// Select returns the directories pnpm-workspace.yaml's `packages` patterns name that
// hold a package.json; a `!` pattern excludes. No manifest means no targets.
func (tsWorkspace) Select(
	ctx context.Context,
	codebase interfaces.Codebase,
	_ *types.TargetSpec,
) ([]string, error) {
	text, err := codebase.Text(ctx, workspaceFile)
	if apperr.Is(err, apperr.CodeNotFound) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	sel, err := packageSelector(text.Bytes)
	if err != nil || len(sel.Globs) == 0 {
		return nil, err
	}

	found, err := codebase.Files(ctx, &sel)
	if err != nil {
		return nil, err
	}

	paths := make([]string, 0, len(found))
	for _, f := range found {
		paths = append(paths, f.Path)
	}

	return dirsOf(paths), nil
}

// packageSelector turns the workspace's package patterns into a package.json selector.
func packageSelector(workspace []byte) (types.FileSelector, error) {
	var item struct {
		// Packages are the workspace's package directory patterns.
		Packages []string `yaml:"packages"`
	}

	err := yaml.Unmarshal(workspace, &item)
	if err != nil {
		return types.FileSelector{}, apperr.Wrap(
			err, apperr.CodeInvalidInput, "parse "+workspaceFile,
		)
	}

	sel := types.FileSelector{}

	for _, text := range item.Packages {
		if neg, ok := strings.CutPrefix(text, "!"); ok {
			sel.Exclude = append(sel.Exclude, path.Join(neg, "package.json"))

			continue
		}

		sel.Globs = append(sel.Globs, path.Join(text, "package.json"))
	}

	return sel, nil
}
