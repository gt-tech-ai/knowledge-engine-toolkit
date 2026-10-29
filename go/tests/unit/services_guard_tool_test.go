package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	clientcodebase "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/codebase"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
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
	ok := types.RenderSpec{ChartDir: "chart", Set: map[string]string{"kind": "hpa"}}
	bad := types.RenderSpec{ChartDir: "chart", Set: map[string]string{"kind": "x"}}
	r := mocks.NewMockRenderer(ctrl)
	r.EXPECT().Render(gomock.Any(), ok).Return([]types.Object{
		{Kind: "Deployment", Doc: map[string]any{"kind": "Deployment"}},
		{Kind: "Service", Doc: map[string]any{"kind": "Service"}},
	}, nil).AnyTimes()
	r.EXPECT().Render(gomock.Any(), bad).
		Return(nil, apperr.New(apperr.CodeUpstream, "unknown kind")).AnyTimes()
	cb, err := clientcodebase.NewFromConfig(t.Context(), clientcodebase.Config{},
		clientcodebase.Deps{Decorations: fixtures.Decorations(), Renderer: r})
	require.NoError(t, err)

	runner := mocks.NewMockCommandRunner(ctrl)
	runner.EXPECT().
		RunBufferedWithInput(gomock.Any(), "/repo", gomock.Nil(),
			[]byte("kind: Deployment\n---\nkind: Service\n"),
			"kubeconform", "-strict", "-summary", "-kubernetes-version", "1.30").
		Return(interfaces.CmdResult{}).Times(1)
	runner.EXPECT().
		RunBufferedWithInput(gomock.Any(), "/repo", gomock.Nil(), gomock.Any(),
			"kubeconform", "-strict", "-summary", "-kubernetes-version", "1.29").
		Return(interfaces.CmdResult{
			Stdout: []byte("Deployment is invalid"),
			Err:    apperr.Sentinel("exit status 1"),
		}).Times(1)
	each := func(context.Context, interfaces.Codebase) ([]map[string]string, error) {
		return []map[string]string{{"k8s": "1.29"}}, nil
	}
	row := func(name string, spec types.RenderSpec, k8s string) types.GuardRow {
		return types.GuardRow{
			Name: name, Kind: types.GuardTool, Tool: "kubeconform", Render: spec,
			Args: []string{"-strict", "-summary", "-kubernetes-version", k8s},
		}
	}
	rows := []types.GuardRow{
		row("valid", ok, "1.30"),
		func() types.GuardRow { r := row("invalid", ok, "{k8s}"); r.Each = "k8s"; return r }(),
		row("unrendered", bad, "1.30"),
	}
	gs, err := guard.NewFromConfig(rows, guard.Deps{
		Runner: runner, Root: "/repo", Expanders: map[string]guard.Expander{"k8s": each},
	})
	require.NoError(t, err)
	v := map[string]types.Verdict{}
	for _, g := range gs {
		v[g.Name()] = g.Run(t.Context(), cb)
	}

	assert.Equal(t, types.StatusPass, v["valid"].Status)
	require.Equal(t, types.StatusFail, v["invalid"].Status)
	assert.Contains(t, v["invalid"].Detail, "kubeconform rejected chart")
	assert.Contains(t, v["invalid"].Detail, "Deployment is invalid")
	require.Equal(t, types.StatusFail, v["unrendered"].Status)
	assert.Contains(t, v["unrendered"].Detail, "render chart: ")

	_, err = guard.NewFromConfig([]types.GuardRow{{Name: "t", Kind: types.GuardTool}},
		guard.Deps{})
	require.Error(t, err, "a tool row needs a tool")
	gs, err = guard.NewFromConfig(
		[]types.GuardRow{row("norunner", ok, "1")},
		guard.Deps{},
	)
	require.NoError(t, err)
	assert.Equal(t, types.StatusFail, gs[0].Run(t.Context(), cb).Status)
}
