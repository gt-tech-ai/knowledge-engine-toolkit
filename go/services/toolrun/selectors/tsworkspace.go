package selectors

import (
	"context"
	"path"
	"strings"

	"go.yaml.in/yaml/v3"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
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
	cb interfaces.Codebase,
	_ types.TargetSpec,
) ([]string, error) {
	text, err := cb.Text(ctx, workspaceFile)
	if apperr.Is(err, apperr.CodeNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ws struct {
		// Packages are the workspace's package directory patterns.
		Packages []string `yaml:"packages"`
	}
	if err := yaml.Unmarshal(text.Bytes, &ws); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "parse "+workspaceFile)
	}
	sel := types.FileSelector{}
	for _, p := range ws.Packages {
		if neg, ok := strings.CutPrefix(p, "!"); ok {
			sel.Exclude = append(sel.Exclude, path.Join(neg, "package.json"))
			continue
		}
		sel.Globs = append(sel.Globs, path.Join(p, "package.json"))
	}
	if len(sel.Globs) == 0 {
		return nil, nil
	}
	found, err := cb.Files(ctx, sel)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(found))
	for _, f := range found {
		paths = append(paths, f.Path)
	}
	return dirsOf(paths), nil
}
