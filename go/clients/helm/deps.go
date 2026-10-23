package helm

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/codebase"
)

// depsKey prefixes the snapshot memo key of a chart's dependency build.
const depsKey = "helm|deps|"

// BuildDeps runs `helm dependency build` on chartDir through c once per snapshot s:
// concurrent and later callers on the same chart wait for and share the first build's
// result, its error included. The Renderer builds through the same key, so a chart a
// caller has already built is not built again before its first render.
func BuildDeps(
	ctx context.Context,
	c interfaces.HelmClient,
	s *codebase.Snapshot,
	chartDir string,
) error {
	_, err := s.Memo(ctx, depsKey+chartDir, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, c.DependencyBuild(ctx, chartDir)
	})
	return err
}
