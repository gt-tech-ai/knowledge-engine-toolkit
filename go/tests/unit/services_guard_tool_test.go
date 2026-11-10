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
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
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
	renderSpec := fixtures.Build(func(row *types.RenderSpec) {
		row.ChartDir = textChart
		row.Set = map[string]string{textKind: "hpa"}
	})
	bad := fixtures.Build(func(row *types.RenderSpec) {
		row.ChartDir = textChart
		row.Set = map[string]string{textKind: textX}
	})
	renderer := mocks.NewMockRenderer(ctrl)
	renderer.EXPECT().Render(gomock.Any(), any(&renderSpec)).Return([]types.Object{
		fixtures.Build(func(row *types.Object) {
			row.Kind = textDeployment
			row.Doc = map[string]any{textKind: textDeployment}
		}),
		fixtures.Build(func(row *types.Object) {
			row.Kind = "Service"
			row.Doc = map[string]any{textKind: "Service"}
		}),
	}, nil).AnyTimes()
	renderer.EXPECT().Render(gomock.Any(), any(&bad)).
		Return(nil, apperr.New(apperr.CodeUpstream, "unknown kind")).AnyTimes()

	codebase, err := clientcodebase.NewFromConfig(
		t.Context(),
		stdx.Zero[clientcodebase.Config](),
		fixtures.BuildRef(func(row *clientcodebase.Deps) {
			row.Decorations = fixtures.Decorations()
			row.Renderer = renderer
		}),
	)
	require.NoError(t, err)

	runner := mocks.NewMockCommandRunner(ctrl)
	runner.EXPECT().
		RunBufferedWithInput(gomock.Any(), textRepo, gomock.Nil(),
			[]byte("kind: Deployment\n---\nkind: Service\n"),
			"kubeconform", "-strict", "-summary", "-kubernetes-version", "1.30").
		Return(stdx.Zero[interfaces.CmdResult]()).Times(1)
	runner.EXPECT().
		RunBufferedWithInput(gomock.Any(), textRepo, gomock.Nil(), gomock.Any(),
			"kubeconform", "-strict", "-summary", "-kubernetes-version", "1.29").
		Return(fixtures.Build(func(row *interfaces.CmdResult) {
			row.Stdout = []byte("Deployment is invalid")
			row.Err = apperr.Sentinel("exit status 1")
		})).Times(1)

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

	each := func(context.Context, interfaces.Codebase) ([]map[string]string, error) {
		return []map[string]string{{textK8s: "1.29"}}, nil
	}
	row := func(name string, spec types.RenderSpec, k8s string) types.GuardRow {
		return fixtures.Build(func(row *types.GuardRow) {
			row.Name = name
			row.Kind = types.GuardTool
			row.Tool = "kubeconform"
			row.Render = spec
			row.Args = []string{"-strict", "-summary", "-kubernetes-version", k8s}
		})
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

	testServicesGuardToolVerdictsChecksStatuses(t,
		codebase,
		runner,
		&renderSpec,
		each,
		row,
		rows,
	)
}

// testServicesGuardToolVerdictsChecksStatuses continues testServicesGuardToolVerdicts.
func testServicesGuardToolVerdictsChecksStatuses(t *testing.T,
	codebase interfaces.Codebase,
	runner *mocks.MockCommandRunner,
	renderSpec *types.RenderSpec,
	each func(context.Context, interfaces.Codebase) ([]map[string]string, error),
	row func(name string, spec types.RenderSpec, k8s string) types.GuardRow,
	rows []types.GuardRow,
) {
	t.Helper()

	guards, err := guard.NewFromConfig(rows, fixtures.BuildRef(func(row *guard.Deps) {
		row.Runner = runner
		row.Root = textRepo
		row.Expanders = map[string]guard.Expander{textK8s: each}
	}))
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

	_, err = guard.NewFromConfig(
		[]types.GuardRow{
			fixtures.Build(func(row *types.GuardRow) {
				row.Name = textT
				row.Kind = types.GuardTool
			}),
		},
		new(guard.Deps),
	)
	require.Error(t, err, "a tool row needs a tool")

	guards, err = guard.NewFromConfig(
		[]types.GuardRow{row("norunner", *renderSpec, "1")},
		new(guard.Deps),
	)
	require.NoError(t, err)
	assert.Equal(t, types.StatusFail, guards[0].Run(t.Context(), codebase).Status)
}
