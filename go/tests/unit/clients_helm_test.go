package unit_test

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/helm"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/codebase"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// helmManifest is a rendered chart: two objects, an empty document between them.
const helmManifest = "---\n# Source: api/templates/deploy.yaml\n" +
	"apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: api\n  namespace: app\n" +
	"---\n# Source: api/templates/empty.yaml\n" +
	"---\n# Source: api/templates/svc.yaml\nkind: Service\nmetadata:\n  name: api\n"

// TestClientsHelm_Exec tests the helm binary backend's command lines.
//
// Why this test is important:
//   - Values precedence is helm's: files in order, then --set; a reordered flag renders
//     a different manifest than the cluster gets.
//
// What it tests:
//   - template passes release, chart, namespace, values files in order and sorted
//     --set flags in the root; an empty release is the chart's base name; dependency
//     build runs with a private repository cache and config (so concurrent builds of
//     different charts never share helm's per-user index files), removed afterwards;
//     lint runs its subcommand; a failure is UPSTREAM.
func TestClientsHelm_Exec(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	r := mocks.NewMockCommandRunner(gomock.NewController(t))
	c, err := helm.NewFromConfig(ctx, helm.Config{Kind: helm.KindExec},
		helm.Deps{Decorations: fixtures.Decorations(), Runner: r, Root: "/repo"})
	require.NoError(t, err)
	spec := types.RenderSpec{
		ChartDir:    "charts/api",
		Namespace:   "app",
		ValuesFiles: []string{"v1.yaml", "v0.yaml"},
		Set:         map[string]string{"b": "2", "a": "1"},
	}
	r.EXPECT().RunBuffered(gomock.Any(), "/repo", "helm", "template", "api", "charts/api",
		"--namespace", "app", "-f", "v1.yaml", "-f", "v0.yaml", "--set", "a=1", "--set", "b=2").
		Return(interfaces.CmdResult{Stdout: []byte("kind: X\n")})
	out, err := c.Template(ctx, spec)
	require.NoError(t, err)
	assert.Equal(t, "kind: X\n", string(out))

	var cacheDir string
	r.EXPECT().
		RunBuffered(gomock.Any(), "/repo", "helm", "dependency", "build", "charts/api",
			"--repository-cache", gomock.Any(), "--repository-config", gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _ string, args ...string) interfaces.CmdResult {
			cacheDir = args[4]
			assert.DirExists(t, cacheDir, "the private cache exists during the build")
			assert.Equal(t, filepath.Dir(cacheDir), filepath.Dir(args[6]),
				"cache and config share one private home")
			return interfaces.CmdResult{}
		})
	require.NoError(t, c.DependencyBuild(ctx, "charts/api"))
	assert.NoDirExists(t, filepath.Dir(cacheDir), "the private home is removed")

	r.EXPECT().RunBuffered(gomock.Any(), "/repo", "helm", "lint", "charts/api").
		Return(exitWith(1, "Error: chart metadata is missing"))
	err = c.Lint(ctx, types.RenderSpec{ChartDir: "charts/api"})
	assert.Equal(t, apperr.CodeUpstream, apperr.Code(err))
}

// TestClientsHelm_Renderer tests the Renderer adapter.
//
// Why this test is important:
//   - Manifest checks render the same charts many times per run; each extra helm call
//     costs seconds, and a concurrent dependency build on one chart races on its
//     charts/ directory.
//
// What it tests:
//   - Concurrent renders of one spec call template once; two specs on one chart build
//     its dependencies once; objects carry kind, name, namespace, source and index,
//     with empty documents dropped.
func TestClientsHelm_Renderer(t *testing.T) {
	t.Parallel()
	c := mocks.NewMockHelmClient(gomock.NewController(t))
	var templates atomic.Int32
	c.EXPECT().DependencyBuild(gomock.Any(), "charts/api").Return(nil).Times(1)
	c.EXPECT().Template(gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, types.RenderSpec) ([]byte, error) {
			templates.Add(1)
			return []byte(helmManifest), nil
		}).Times(2)
	rr := helm.NewRenderer(c, codebase.NewSnapshot("t"))
	spec := types.RenderSpec{ChartDir: "charts/api"}

	var wg sync.WaitGroup
	results := make([][]types.Object, 8)
	errs := make([]error, 8)
	for i := range results {
		wg.Go(func() { results[i], errs[i] = rr.Render(t.Context(), spec) })
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), templates.Load())
	objs := results[0]
	require.Len(t, objs, 2)
	assert.Equal(t, types.Object{
		Doc: objs[0].Doc, Kind: "Deployment", Name: "api", Namespace: "app",
		Source: "api/templates/deploy.yaml", Index: 0,
	}, objs[0])
	assert.Equal(t, "api/templates/svc.yaml", objs[1].Source)
	assert.Equal(t, 1, objs[1].Index)

	_, err := rr.Render(
		t.Context(),
		types.RenderSpec{ChartDir: "charts/api", Release: "two"},
	)
	require.NoError(t, err)
}
