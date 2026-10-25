package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/guard"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/suite"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// TestServicesSuite_Tools tests the tools a suite's selection shells.
//
// Why this test is important:
//   - CI installs what the suites it runs need, computed from the catalog: a run row's
//     tool, a guard row's tool (plus the renderer's when it renders), a registered
//     step's tools. A tool missing from that answer is a tool CI never installs.
//
// What it tests:
//   - Tools unions the selected entries' tools, sorted and unique, under the suite's
//     flags; an entry the flags do not select contributes nothing; an unknown flag is
//     INVALID_INPUT.
func TestServicesSuite_Tools(t *testing.T) {
	t.Parallel()
	gs, err := guard.NewFromConfig([]types.GuardRow{
		{
			Name: "validate", Verb: "lint", Kind: types.GuardGo, Tags: []string{"infra"},
			Tool: "terraform",
		},
		{
			Name:   "rendered",
			Verb:   "lint",
			Kind:   types.GuardExpr,
			Tags:   []string{"infra"},
			Expr:   "true",
			Render: types.RenderSpec{ChartDir: "chart"},
		},
	}, guard.Deps{
		Go: map[string]guard.GoGuard{"validate": func(
			context.Context, interfaces.Codebase, *types.GuardRow, map[string]string,
		) ([]types.Finding, error) {
			return nil, nil
		}},
		RenderTool: "helm",
	})
	require.NoError(t, err)
	catalog := append(append(suite.Runs(nil, nil, []types.ToolRun{
		{
			Name: "golangci-lint",
			Verb: "lint",
			Tags: []string{"go"},
			Tool: "golangci-lint",
		},
		{Name: "hadolint", Verb: "lint", Tags: []string{"infra"}, Tool: "hadolint"},
		{Name: "tflint", Verb: "lint", Tags: []string{"infra"}, Tool: "tflint"},
	}), suite.Steps(map[string]suite.Step{
		"lint.kube-linter": {
			Group: "Lint", Tags: []string{"infra"},
			Tools: []string{"kube-linter", "helm"},
		},
	})...), suite.Guards(gs, nil)...)
	s := types.Suite{
		Title:  "Lint",
		Select: types.Selection{Verbs: []string{"lint"}, Tags: []string{"go"}},
		Phases: []types.Phase{{Group: "lint"}, {Group: "Lint"}},
		Selectors: map[string]types.Selection{
			"go":    {Verbs: []string{"lint"}, Tags: []string{"go"}},
			"infra": {Verbs: []string{"lint"}, Tags: []string{"infra"}},
		},
		Steps: []string{"lint.kube-linter"},
	}

	got, err := suite.Tools(s, catalog, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"golangci-lint"}, got)

	got, err = suite.Tools(s, catalog, []string{"infra"})
	require.NoError(t, err)
	assert.Equal(t,
		[]string{"hadolint", "helm", "kube-linter", "terraform", "tflint"}, got)

	_, err = suite.Tools(s, catalog, []string{"rust"})
	require.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// TestServicesSuite_StrictFailsSkippedGuard tests a skipped guard under strict options.
//
// Why this test is important:
//   - CI runs the validation suites with --strict so a tool missing from the runner
//     fails the job instead of silently no-op'ing a gate; a guard whose tool is absent
//     skips, and must fail there too — while a skip that is a cached pass must not.
//
// What it tests:
//   - a Go guard reporting UNAVAILABLE is Skip by default and Fail under Strict, its
//     error the guard's message; a cached skip stays Skip under Strict.
func TestServicesSuite_StrictFailsSkippedGuard(t *testing.T) {
	t.Parallel()
	gs, err := guard.NewFromConfig([]types.GuardRow{
		{Name: "tool", Verb: "check", Kind: types.GuardGo},
		{
			Name: "cached", Verb: "check", Kind: types.GuardGo,
			Inputs: types.FileSelector{Roots: []string{"deploy"}}, Tool: "x",
		},
	}, guard.Deps{
		Go: map[string]guard.GoGuard{
			"tool": func(
				context.Context, interfaces.Codebase, *types.GuardRow, map[string]string,
			) ([]types.Finding, error) {
				return nil, apperr.New(
					apperr.CodeUnavailable,
					"terraform is not installed",
				)
			},
			"cached": func(
				context.Context, interfaces.Codebase, *types.GuardRow, map[string]string,
			) ([]types.Finding, error) {
				return nil, nil
			},
		},
		Cache: cachedGuardDeps(t, nil, "").Cache,
	})
	require.NoError(t, err)
	cb := memoryCodebase(t, guardFiles())
	run := func(strict bool, i int) types.StepResult {
		job := suite.Guards(gs, cb)[i].Job(suite.Env{
			Decs: fixtures.Decorations(), Opts: types.RunOptions{Strict: strict},
		})
		res, err := job.Execute(t.Context(), nil, "/r")
		require.NoError(t, err)
		require.Len(t, res, 1)
		return res[0]
	}
	assert.Equal(t, types.StatusSkip, run(false, 0).Status)
	strict := run(true, 0)
	assert.Equal(t, types.StatusFail, strict.Status)
	assert.Contains(t, strict.Error, "terraform is not installed")

	assert.Equal(
		t,
		types.StatusPass,
		run(true, 1).Status,
		"the first run records the pass",
	)
	cached := run(true, 1)
	assert.Equal(t, types.StatusSkip, cached.Status, "a cached pass is no strict failure")
	assert.Equal(t, types.CachedDetail, string(cached.Detail))
}
