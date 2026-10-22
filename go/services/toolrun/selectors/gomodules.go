package selectors

import (
	"context"
	"path"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// goModules is KindGoModules.
type goModules struct{}

// Select returns the codebase's Go modules; with spec.WithPackages, only those that own
// a Go file (one whose nearest enclosing module is that module).
func (goModules) Select(
	ctx context.Context,
	cb interfaces.Codebase,
	spec types.TargetSpec,
) ([]string, error) {
	mods, err := cb.Go().Modules(ctx)
	if err != nil || !spec.WithPackages {
		return mods, err
	}
	gofiles, err := cb.Files(ctx, types.FileSelector{
		Exts: []string{".go"}, IncludeGenerated: true,
	})
	if err != nil {
		return nil, err
	}
	owns := map[string]bool{}
	for _, f := range gofiles {
		if m, ok := owner(mods, f.Path); ok {
			owns[m] = true
		}
	}
	out := make([]string, 0, len(mods))
	for _, m := range mods {
		if owns[m] {
			out = append(out, m)
		}
	}
	return out, nil
}

// owner returns the deepest module in mods that contains p.
func owner(mods []string, p string) (string, bool) {
	best, found := "", false
	for _, m := range mods {
		m = path.Clean(m)
		if (m == "." || strings.HasPrefix(p, m+"/")) && (!found || len(m) > len(best)) {
			best, found = m, true
		}
	}
	return best, found
}
