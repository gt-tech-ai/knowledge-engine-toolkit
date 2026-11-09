package unit_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/helm"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/codebase"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestBuildDeps_OncePerChartPerSnapshot tests the shared chart-dependency build.
//
// Why this test is important:
//   - helm dependency build writes into the chart directory; two concurrent builds of
//     one chart race, and a build per caller repeats minutes of network work per run.
//
// What it tests:
//   - Eight concurrent BuildDeps on one chart build it once; two charts build once
//     each; a failure reaches every caller of that snapshot; a Render after BuildDeps
//     does not build again.
func TestBuildDeps_OncePerChartPerSnapshot(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	c := mocks.NewMockHelmClient(gomock.NewController(t))
	snap := codebase.NewSnapshot(textT)

	c.EXPECT().DependencyBuild(gomock.Any(), textChartsAPI).Return(nil).Times(1)
	c.EXPECT().DependencyBuild(gomock.Any(), "charts/web").Return(nil).Times(1)

	var wg sync.WaitGroup

	for range iterations8 {
		wg.Go(func() { assert.NoError(t, helm.BuildDeps(ctx, c, snap, textChartsAPI)) })
	}

	wg.Wait()
	require.NoError(t, helm.BuildDeps(ctx, c, snap, "charts/web"))

	c.EXPECT().Template(gomock.Any(), gomock.Any()).Return([]byte("kind: X\n"), nil)

	_, err := helm.NewRenderer(c, snap).
		Render(ctx, types.RenderSpec{ChartDir: textChartsAPI})
	require.NoError(t, err)

	bad := codebase.NewSnapshot(textBad)

	c.EXPECT().DependencyBuild(gomock.Any(), "charts/bad").
		Return(apperr.New(apperr.CodeUpstream, textBoom)).Times(1)

	for range iterations2 {
		err := helm.BuildDeps(ctx, c, bad, "charts/bad")
		assert.Equal(t, apperr.CodeUpstream, apperr.Code(err))
	}
}
