package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	clientcodebase "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/codebase"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/guard"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestServicesGuard_Tool tests the tool kind: a rendered chart validated by a tool.
//
// Why this test is important:
//   - Manifest validators (kubeconform) read the objects a chart renders; the product
//     should name the tool and its arguments in data, never write a Go guard that
//     renders, serializes and shells per tool.
//
// What it tests:
//   - The row's tool runs in the root with its args (placeholders expanded from each)
//     and the rendered objects on stdin as YAML documents; exit 0 passes.
//   - A failing tool is one finding carrying its output; a render failure is a finding
//     naming the chart, without running the tool.
//   - Building rejects a tool row with no tool, and a run with no runner fails the row.
func TestServicesGuard_Tool(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	renderSpec := types.RenderSpec{
		ChartDir: textChart,
		Set:      map[string]string{textKind: "hpa"},
	}
	bad := types.RenderSpec{ChartDir: textChart, Set: map[string]string{textKind: textX}}
	renderer := mocks.NewMockRenderer(ctrl)
	renderer.EXPECT().Render(gomock.Any(), any(&renderSpec)).Return([]types.Object{
		{Kind: textDeployment, Doc: map[string]any{textKind: textDeployment}},
		{Kind: "Service", Doc: map[string]any{textKind: "Service"}},
	}, nil).AnyTimes()
	renderer.EXPECT().Render(gomock.Any(), any(&bad)).
		Return(nil, apperr.New(apperr.CodeUpstream, "unknown kind")).AnyTimes()

	codebase, err := clientcodebase.NewFromConfig(t.Context(), clientcodebase.Config{},
		&clientcodebase.Deps{Decorations: fixtures.Decorations(), Renderer: renderer})
	require.NoError(t, err)

	runner := mocks.NewMockCommandRunner(ctrl)
	runner.EXPECT().
		RunBufferedWithInput(gomock.Any(), textRepo, gomock.Nil(),
			[]byte("kind: Deployment\n---\nkind: Service\n"),
			"kubeconform", "-strict", "-summary", "-kubernetes-version", "1.30").
		Return(interfaces.CmdResult{}).Times(1)
	runner.EXPECT().
		RunBufferedWithInput(gomock.Any(), textRepo, gomock.Nil(), gomock.Any(),
			"kubeconform", "-strict", "-summary", "-kubernetes-version", "1.29").
		Return(interfaces.CmdResult{
			Stdout: []byte("Deployment is invalid"),
			Err:    apperr.Sentinel("exit status 1"),
		}).Times(1)

	testServicesGuardToolVerdicts(t, &renderSpec, &bad, codebase, runner)
}

// testServicesGuardToolVerdicts continues TestServicesGuard_Tool: verdicts.
func testServicesGuardToolVerdicts(
	t *testing.T,
	okRef, bad *types.RenderSpec,
	codebase interfaces.Codebase,
	runner *mocks.MockCommandRunner,
) {
	t.Helper()

	renderSpec := *okRef

	var err error

	each := func(context.Context, interfaces.Codebase) ([]map[string]string, error) {
		return []map[string]string{{textK8s: "1.29"}}, nil
	}
	row := func(name string, spec types.RenderSpec, k8s string) types.GuardRow {
		return types.GuardRow{
			Name: name, Kind: types.GuardTool, Tool: "kubeconform", Render: spec,
			Args: []string{"-strict", "-summary", "-kubernetes-version", k8s},
		}
	}
	rows := []types.GuardRow{
		row("valid", renderSpec, "1.30"),
		func() types.GuardRow {
			r := row("invalid", renderSpec, "{k8s}")

			r.Each = textK8s

			return r
		}(),
		row("unrendered", *bad, "1.30"),
	}
	guards, err := guard.NewFromConfig(rows, &guard.Deps{
		Runner:    runner,
		Root:      textRepo,
		Expanders: map[string]guard.Expander{textK8s: each},
	})
	require.NoError(t, err)

	statusByName := map[string]types.Verdict{}
	for _, g := range guards {
		statusByName[g.Name()] = g.Run(t.Context(), codebase)
	}

	assert.Equal(t, types.StatusPass, statusByName["valid"].Status)
	require.Equal(t, types.StatusFail, statusByName["invalid"].Status)
	assert.Contains(t, statusByName["invalid"].Detail, "kubeconform rejected chart")
	assert.Contains(t, statusByName["invalid"].Detail, "Deployment is invalid")
	require.Equal(t, types.StatusFail, statusByName["unrendered"].Status)
	assert.Contains(t, statusByName["unrendered"].Detail, "render chart: ")

	_, err = guard.NewFromConfig([]types.GuardRow{{Name: textT, Kind: types.GuardTool}},
		&guard.Deps{})
	require.Error(t, err, "a tool row needs a tool")

	guards, err = guard.NewFromConfig(
		[]types.GuardRow{row("norunner", renderSpec, "1")},
		&guard.Deps{},
	)
	require.NoError(t, err)
	assert.Equal(t, types.StatusFail, guards[0].Run(t.Context(), codebase).Status)
}
