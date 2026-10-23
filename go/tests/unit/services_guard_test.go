package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	clientcodebase "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/codebase"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/guard"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/suite"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// guardFiles returns the repository the guard tests run over.
func guardFiles() map[string]string {
	return map[string]string{
		"deploy/a.yaml": "# image: latest\nimage: app:1.2\nreplicas: 2\n",
		"deploy/b.yaml": "image: app:latest\nreplicas: 1\n",
		"infra/main.tf": "// TODO\nresource \"x\" {}\n",
		"policy/p.yaml": "kind: AuthorizationPolicy\nspec:\n  rules:\n" +
			"  - from:\n    - source:\n        principals: [kong, api]\n",
	}
}

// runGuards builds rows and runs each guard over the guard repository.
func runGuards(
	t *testing.T,
	rows []types.GuardRow,
	deps guard.Deps,
) map[string]types.Verdict {
	t.Helper()
	gs, err := guard.NewFromConfig(rows, deps)
	require.NoError(t, err)
	cb := memoryCodebase(t, guardFiles())
	out := map[string]types.Verdict{}
	for _, g := range gs {
		out[g.Name()] = g.Run(t.Context(), cb)
	}
	return out
}

// TestServicesGuard_Match tests the match kind.
//
// Why this test is important:
//   - most repository guards are line patterns; a comment that satisfies a require or a
//     forbid hit with no location would make them useless.
//
// What it tests:
//   - a forbid hit is reported with file:line; skip_comments ignores per-type comments;
//     names split require patterns into one guard each; a literal pattern is a substring;
//     a selector with no file fails unless optional.
func TestServicesGuard_Match(t *testing.T) {
	t.Parallel()
	deploy := types.FileSelector{Globs: []string{"deploy/*.yaml"}}
	vs := runGuards(t, []types.GuardRow{
		{
			Name: "no-latest", Kind: types.GuardMatch, Files: deploy, SkipComments: true,
			Forbid: []string{`:latest\b`},
		},
		{
			Name: "pinned", Kind: types.GuardMatch, Files: deploy, Literal: true,
			Names:   []string{"has-image", "has-replicas-2"},
			Require: []string{"image:", "replicas: 2"},
		},
		{
			Name:         "tf",
			Kind:         types.GuardMatch,
			SkipComments: true,
			Files: types.FileSelector{
				Paths: []string{"infra/main.tf"},
			},
			Forbid: []string{"TODO"},
		},
		{
			Name:  "none",
			Kind:  types.GuardMatch,
			Files: types.FileSelector{Paths: []string{"x"}},
		},
		{
			Name: "none-ok", Kind: types.GuardMatch, Optional: true,
			Files: types.FileSelector{Paths: []string{"x"}},
		},
	}, guard.Deps{})

	require.Equal(t, types.StatusFail, vs["no-latest"].Status)
	require.Len(t, vs["no-latest"].Findings, 1)
	assert.Equal(t, "deploy/b.yaml", vs["no-latest"].Findings[0].File)
	assert.Equal(t, 1, vs["no-latest"].Findings[0].Line)
	assert.Contains(t, vs["no-latest"].Detail, "deploy/b.yaml:1:")
	assert.Equal(t, types.StatusPass, vs["has-image"].Status)
	require.Equal(t, types.StatusFail, vs["has-replicas-2"].Status)
	assert.Contains(
		t,
		vs["has-replicas-2"].Detail,
		`deploy/b.yaml does not contain "replicas: 2"`,
	)
	assert.Equal(t, types.StatusPass, vs["tf"].Status)
	assert.Equal(t, types.StatusFail, vs["none"].Status)
	assert.Equal(t, types.StatusPass, vs["none-ok"].Status)
}

// TestServicesGuard_YAMLAndEach tests the yaml kind under an each expansion.
//
// Why this test is important:
//   - per-target guards (one per chart, per service) are one row over an expander, and
//     its placeholders must reach the selector, the path and the expectation.
//
// What it tests:
//   - each expansion runs the row once per row of values, substituting {placeholders};
//     an unexpected value is a finding at its line; a missing path is a finding.
func TestServicesGuard_YAMLAndEach(t *testing.T) {
	t.Parallel()
	each := func(context.Context, interfaces.Codebase) ([]map[string]string, error) {
		return []map[string]string{{"f": "a", "n": "2"}, {"f": "b", "n": "2"}}, nil
	}
	vs := runGuards(t, []types.GuardRow{
		{
			Name: "replicas", Kind: types.GuardYAML, Each: "deploys", Path: "$.replicas",
			Expect: "{n}", Files: types.FileSelector{Paths: []string{"deploy/{f}.yaml"}},
		},
		{
			Name: "missing", Kind: types.GuardYAML, Path: "$.nope",
			Files: types.FileSelector{Paths: []string{"deploy/a.yaml"}},
		},
	}, guard.Deps{Expanders: map[string]guard.Expander{"deploys": each}})

	require.Equal(t, types.StatusFail, vs["replicas"].Status)
	require.Len(t, vs["replicas"].Findings, 1)
	assert.Equal(t, "deploy/b.yaml", vs["replicas"].Findings[0].File)
	assert.Equal(t, 2, vs["replicas"].Findings[0].Line)
	assert.Equal(t, types.StatusFail, vs["missing"].Status)
}

// TestServicesGuard_Expr tests the expr kind.
//
// Why this test is important:
//   - computed checks (an allow list per surface, cross-file consistency) are expressions
//     over decoded documents instead of Go code, so their result shapes must all work.
//
// What it tests:
//   - a bool result passes or fails with the row's message; a list of maps becomes
//     findings with file and line; file text and vars are readable; a CEL error at build
//     time is CodeInvalidInput.
func TestServicesGuard_Expr(t *testing.T) {
	t.Parallel()
	policies := types.FileSelector{Globs: []string{"policy/*.yaml"}}
	vs := runGuards(t, []types.GuardRow{
		{
			Name: "two-principals", Kind: types.GuardExpr, Files: policies,
			Message: "too many principals",
			Expr: `docs.all(d, d.doc.spec.rules.all(r, r.from.all(f,
				size(f.source.principals) <= 1)))`,
		},
		{
			Name:  "listed",
			Kind:  types.GuardExpr,
			Files: policies,
			Expr: `docs.filter(d, !("kong" in d.doc.spec.rules[0].from[0].source.principals))
				.map(d, {"file": d.path, "line": 1, "message": "kong missing"})`,
		},
		{
			Name:  "text",
			Kind:  types.GuardExpr,
			Files: types.FileSelector{Globs: []string{"deploy/*"}},
			Expr: `files.filter(p, file[p].text.contains("latest") && p.endsWith("b.yaml"))
				.map(p, {"file": p, "line": 1, "message": "latest"})`,
		},
	}, guard.Deps{})

	require.Equal(t, types.StatusFail, vs["two-principals"].Status)
	assert.Equal(t, "too many principals", vs["two-principals"].Findings[0].Message)
	assert.Equal(t, types.StatusPass, vs["listed"].Status)
	require.Equal(t, types.StatusFail, vs["text"].Status)
	assert.Equal(t, "deploy/b.yaml", vs["text"].Findings[0].File)
	assert.Equal(t, 1, vs["text"].Findings[0].Line)

	_, err := guard.NewFromConfig([]types.GuardRow{
		{Name: "bad", Kind: types.GuardExpr, Expr: "files.("},
	}, guard.Deps{})
	require.Error(t, err)
}

// TestServicesGuard_GoAndValidation tests Go guards and row validation.
//
// Why this test is important:
//   - a go row hands a check the declarative kinds cannot express to product code, and a
//     typo in a row must fail the build, never silently skip.
//
// What it tests:
//   - a go row runs the registered probe, its detail becoming a finding; an unknown kind,
//     an unknown expander and an unregistered go row are errors; the guard becomes a
//     suite entry whose step carries the verdict.
func TestServicesGuard_GoAndValidation(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	probe := mocks.NewMockProbe[interfaces.Codebase](ctrl)
	probe.EXPECT().Run(gomock.Any(), gomock.Any()).
		Return(types.Verdict{Status: types.StatusFail, Detail: "computed"})
	gs, err := guard.NewFromConfig([]types.GuardRow{
		{Name: "computed", Kind: types.GuardGo, Verb: "check", Tags: []string{"k8s"}},
	}, guard.Deps{Go: map[string]interfaces.Probe[interfaces.Codebase]{"computed": probe}})
	require.NoError(t, err)
	assert.Equal(t, types.EntryMeta{
		Name: "computed", Verb: "check", Group: "check",
		Tags: []string{"k8s"},
	}, gs[0].Meta())

	cb, err := clientcodebase.NewFromConfig(t.Context(), clientcodebase.Config{},
		clientcodebase.Deps{Decorations: fixtures.Decorations()})
	require.NoError(t, err)
	entries := suite.Guards(gs, cb)
	require.Len(t, entries, 1)
	assert.Equal(t, suite.KindGuard, entries[0].Kind)
	res, err := entries[0].Job(suite.Env{Decs: fixtures.Decorations()}).
		Execute(t.Context(), nil, "")
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.Equal(t, types.StatusFail, res[0].Status)
	assert.Equal(t, "computed", res[0].Error)

	for _, r := range []types.GuardRow{
		{Name: "k", Kind: "regex"},
		{Name: "e", Kind: types.GuardMatch, Each: "nope"},
		{Name: "g", Kind: types.GuardGo},
		{Name: "y", Kind: types.GuardYAML},
		{Name: "n", Kind: types.GuardMatch, Names: []string{"a"}},
	} {
		_, err := guard.NewFromConfig([]types.GuardRow{r}, guard.Deps{})
		require.Error(t, err, r.Name)
	}
}
