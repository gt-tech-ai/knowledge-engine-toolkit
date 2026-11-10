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
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
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
	helmClient := mocks.NewMockHelmClient(gomock.NewController(t))
	snap := codebase.NewSnapshot(textT)

	helmClient.EXPECT().DependencyBuild(gomock.Any(), textChartsAPI).Return(nil).Times(1)
	helmClient.EXPECT().DependencyBuild(gomock.Any(), "charts/web").Return(nil).Times(1)

	var group sync.WaitGroup

	for range iterationsSnapshot {
		group.Go(
			func() { assert.NoError(t, helm.BuildDeps(ctx, helmClient, snap, textChartsAPI)) },
		)
	}

	group.Wait()
	require.NoError(t, helm.BuildDeps(ctx, helmClient, snap, "charts/web"))

	helmClient.EXPECT().
		Template(gomock.Any(), gomock.Any()).
		Return([]byte("kind: X\n"), nil)

	_, err := helm.NewRenderer(helmClient, snap).
		Render(ctx, fixtures.BuildRef(func(row *types.RenderSpec) {
			row.ChartDir = textChartsAPI
		}))
	require.NoError(t, err)

	bad := codebase.NewSnapshot(textBad)

	helmClient.EXPECT().DependencyBuild(gomock.Any(), "charts/bad").
		Return(apperr.New(apperr.CodeUpstream, textBoom)).Times(1)

	for range iterations {
		err := helm.BuildDeps(ctx, helmClient, bad, "charts/bad")
		assert.Equal(t, apperr.CodeUpstream, apperr.Code(err))
	}
}

const (
	// iterationsSnapshot is the iteration count in TestBuildDeps_OncePerChartPerSnapshot.
	iterationsSnapshot = 8
)
