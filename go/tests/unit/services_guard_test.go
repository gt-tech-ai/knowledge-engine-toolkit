package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	clientcodebase "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/codebase"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
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
		"deploy/a.yaml":     "# image: latest\nimage: app:1.2\nreplicas: 2\n",
		"deploy/b.yaml":     "image: app:latest\nreplicas: 1\n",
		"infra/main.tf":     "// TODO\nresource \"x\" {}\n",
		"notes/broken.yaml": "a: [\n",
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
//   - cel.bind and the regex extension are available;
//   - only the variables the expression references are read, so a name inside a
//     string literal decodes nothing;
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
		{
			Name: "bind", Kind: types.GuardExpr,
			Files: types.FileSelector{Globs: []string{"deploy/*"}},
			Expr: `cel.bind(t, file["deploy/a.yaml"].text,
				regex.extract(t, "image: (app:[0-9.]+)") == optional.of("app:1.2"))`,
		},
		{
			// A variable name inside a string literal is not a reference: the broken
			// YAML file is never decoded.
			Name: "literal", Kind: types.GuardExpr,
			Files: types.FileSelector{Globs: []string{"notes/*"}},
			Expr:  `files.all(p, p != "docs/objects/file")`,
		},
	}, guard.Deps{})

	require.Equal(t, types.StatusFail, vs["two-principals"].Status)
	assert.Equal(t, "too many principals", vs["two-principals"].Findings[0].Message)
	assert.Equal(t, types.StatusPass, vs["listed"].Status)
	require.Equal(t, types.StatusFail, vs["text"].Status)
	assert.Equal(t, "deploy/b.yaml", vs["text"].Findings[0].File)
	assert.Equal(t, 1, vs["text"].Findings[0].Line)
	assert.Equal(t, types.StatusPass, vs["literal"].Status, vs["literal"].Detail)
	assert.Equal(t, types.StatusPass, vs["bind"].Status, vs["bind"].Detail)

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
//   - a go row runs the registered Go guard with its expanded row and placeholders, its
//     findings deciding the verdict, and func lets a second row share it; an unknown
//     kind, an unknown expander and an unregistered go row are errors; the guard becomes
//     suite entry whose step carries the verdict.
func TestServicesGuard_GoAndValidation(t *testing.T) {
	t.Parallel()
	computed := func(
		_ context.Context, _ interfaces.Codebase, row *types.GuardRow, vars map[string]string,
	) ([]types.Finding, error) {
		return []types.Finding{{Message: row.Message + " " + vars["env"]}}, nil
	}
	each := func(context.Context, interfaces.Codebase) ([]map[string]string, error) {
		return []map[string]string{{"env": "a"}}, nil
	}
	gs, err := guard.NewFromConfig([]types.GuardRow{
		{
			Name: "computed", Kind: types.GuardGo, Verb: "check", Tags: []string{"k8s"},
			Each: "envs", Message: "computed in {env}",
		},
		{Name: "shared", Kind: types.GuardGo, Func: "computed", Message: "shared"},
	}, guard.Deps{
		Go:        map[string]guard.GoGuard{"computed": computed},
		Expanders: map[string]guard.Expander{"envs": each},
	})
	require.NoError(t, err)
	assert.Equal(t, types.EntryMeta{
		Name: "computed", Verb: "check", Group: "check",
		Tags: []string{"k8s"},
	}, gs[0].Meta())

	cb, err := clientcodebase.NewFromConfig(t.Context(), clientcodebase.Config{},
		clientcodebase.Deps{Decorations: fixtures.Decorations()})
	require.NoError(t, err)
	entries := suite.Guards(gs[:1], cb)
	require.Len(t, entries, 1)
	assert.Equal(t, suite.KindGuard, entries[0].Kind)
	res, err := entries[0].Job(suite.Env{Decs: fixtures.Decorations()}).
		Execute(t.Context(), nil, "")
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.Equal(t, types.StatusFail, res[0].Status)
	assert.Equal(t, "computed in a a", res[0].Error)
	assert.Equal(t, "shared ", gs[1].Run(t.Context(), cb).Findings[0].Message)

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

// TestServicesGuard_ExprRender tests an expr row over a rendered chart.
//
// Why this test is important:
//   - chart guards assert on rendered objects; a render failure must fail the guard,
//     never pass it silently.
//
// What it tests:
//   - objects carry the rendered kinds; a failing render fails the row with its error.
func TestServicesGuard_ExprRender(t *testing.T) {
	t.Parallel()
	ok := types.RenderSpec{ChartDir: "chart", Set: map[string]string{"kind": "hpa"}}
	bad := types.RenderSpec{ChartDir: "chart", Set: map[string]string{"kind": "x"}}
	r := mocks.NewMockRenderer(gomock.NewController(t))
	r.EXPECT().Render(gomock.Any(), ok).Return([]types.Object{
		{
			Kind: "HorizontalPodAutoscaler",
			Doc:  map[string]any{"kind": "HorizontalPodAutoscaler"},
		},
	}, nil).AnyTimes()
	r.EXPECT().Render(gomock.Any(), bad).
		Return(nil, apperr.New(apperr.CodeUpstream, "helm template: unknown autoscaling.kind")).
		AnyTimes()
	cb, err := clientcodebase.NewFromConfig(t.Context(), clientcodebase.Config{},
		clientcodebase.Deps{Decorations: fixtures.Decorations(), Renderer: r})
	require.NoError(t, err)
	gs, err := guard.NewFromConfig([]types.GuardRow{
		{
			Name: "one-hpa", Kind: types.GuardExpr, Render: ok,
			Expr: `objects.filter(o, o.kind == 'HorizontalPodAutoscaler').size() == 1`,
		},
		{
			Name:   "render-fails",
			Kind:   types.GuardExpr,
			Render: bad,
			Expr:   `objects.size() == 0`,
		},
	}, guard.Deps{})
	require.NoError(t, err)
	vs := map[string]types.Verdict{}
	for _, g := range gs {
		vs[g.Name()] = g.Run(t.Context(), cb)
	}
	assert.Equal(t, types.StatusPass, vs["one-hpa"].Status, vs["one-hpa"].Detail)
	assert.Equal(t, types.StatusFail, vs["render-fails"].Status)
	assert.Contains(t, vs["render-fails"].Detail, "unknown autoscaling.kind")
}

// TestServicesGuard_ExprDecode tests decode(format, text) and the docs activation over
// non-YAML formats.
//
// Why this test is important:
//   - infrastructure checks read Terraform and Alloy as data, and an Alloy pipeline is
//     often embedded as a string inside a Helm values file; without decode a row could
//     only regex that string.
//
// What it tests:
//   - docs over `.tf` files carries the hcl shape (format "hcl", a labelled resource);
//   - decode('alloy', <values string>) yields the same shape the .alloy decoder gives;
//   - an unknown format fails the row with its error.
func TestServicesGuard_ExprDecode(t *testing.T) {
	t.Parallel()
	cb := memoryCodebase(t, map[string]string{
		"tf/main.tf": "resource \"aws_kms_key\" \"cosign\" {\n" +
			"  key_usage = \"SIGN_VERIFY\"\n}\n",
		"obs/values.yaml": "alloy:\n  content: |\n    loki.process \"redact\" {\n" +
			"      forward_to = [loki.write.default.receiver]\n    }\n",
	})
	gs, err := guard.NewFromConfig([]types.GuardRow{
		{
			Name: "hcl",
			Kind: types.GuardExpr,
			Files: types.FileSelector{
				Paths: []string{"tf/main.tf"},
				Exts:  []string{".tf"},
			},
			Expr: `docs.size() == 1 && docs[0].format == 'hcl' &&
				docs[0].doc.resource.aws_kms_key.cosign[0].key_usage == 'SIGN_VERIFY'`,
		},
		{
			Name: "embedded", Kind: types.GuardExpr,
			Files: types.FileSelector{Paths: []string{"obs/values.yaml"}},
			Expr: `decode('alloy', docs[0].doc.alloy.content)['loki.process']['redact'][0]
				.forward_to == ['loki.write.default.receiver']`,
		},
		{
			Name: "unknown", Kind: types.GuardExpr,
			Files: types.FileSelector{Paths: []string{"obs/values.yaml"}},
			Expr:  `decode('ini', docs[0].doc.alloy.content) != null`,
		},
	}, guard.Deps{})
	require.NoError(t, err)
	vs := map[string]types.Verdict{}
	for _, g := range gs {
		vs[g.Name()] = g.Run(t.Context(), cb)
	}
	assert.Equal(t, types.StatusPass, vs["hcl"].Status, vs["hcl"].Detail)
	assert.Equal(t, types.StatusPass, vs["embedded"].Status, vs["embedded"].Detail)
	assert.Equal(t, types.StatusFail, vs["unknown"].Status)
	assert.Contains(t, vs["unknown"].Detail, "unknown format ini")
}

// TestServicesGuard_ExprTOMLAndNow tests the toml documents and the now timestamp an
// expression reads.
//
// Why this test is important:
//   - suppression files with an expiry (osv-scanner's TOML ignore list) can only be
//     checked as data if an expression can decode TOML and compare a date against the
//     current time; without both, each needs a Go guard.
//
// What it tests:
//   - docs over a `.toml` file carries format "toml" and its datetimes as timestamps;
//   - now is Deps.Now's time, so an expiry before it fails and one after it passes;
//   - decode('toml', text) decodes an embedded string, a local date as a timestamp.
func TestServicesGuard_ExprTOMLAndNow(t *testing.T) {
	t.Parallel()
	cb := memoryCodebase(t, map[string]string{
		"osv.toml": "[[IgnoredVulns]]\nid = \"GHSA-1\"\n" +
			"ignoreUntil = 2026-06-01T00:00:00Z\n",
		"cfg.yaml": "embedded: |\n  until = 2026-01-15\n",
	})
	rows := []types.GuardRow{
		{
			Name: "unexpired",
			Kind: types.GuardExpr,
			Files: types.FileSelector{
				Paths: []string{"osv.toml"},
				Exts:  []string{".toml"},
			},
			Expr: `docs.size() == 1 && docs[0].format == 'toml' &&
				docs[0].doc.IgnoredVulns.all(e, e.ignoreUntil > now)`,
		},
		{
			Name: "embedded", Kind: types.GuardExpr,
			Files: types.FileSelector{Paths: []string{"cfg.yaml"}},
			Expr:  `decode('toml', docs[0].doc.embedded).until < now`,
		},
	}
	run := func(now time.Time) map[string]types.Verdict {
		gs, err := guard.NewFromConfig(
			rows,
			guard.Deps{Now: func() time.Time { return now }},
		)
		require.NoError(t, err)
		vs := map[string]types.Verdict{}
		for _, g := range gs {
			vs[g.Name()] = g.Run(t.Context(), cb)
		}
		return vs
	}

	before := run(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	assert.Equal(
		t,
		types.StatusPass,
		before["unexpired"].Status,
		before["unexpired"].Detail,
	)
	assert.Equal(
		t,
		types.StatusPass,
		before["embedded"].Status,
		before["embedded"].Detail,
	)

	after := run(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	assert.Equal(
		t,
		types.StatusFail,
		after["unexpired"].Status,
		after["unexpired"].Detail,
	)
}

// TestServicesGuard_GoUnavailableSkips tests that a Go guard reporting CodeUnavailable
// skips rather than fails.
//
// Why this test is important:
//   - a Go guard that shells out to a tool (terraform, promtool) must behave like a
//     tool-run row on a machine without the tool: skipped, so the suite stays green
//     locally and red only where the tool is pinned (CI).
//
// What it tests:
//   - a CodeUnavailable error yields StatusSkip carrying the error text; any other error
//     still fails.
func TestServicesGuard_GoUnavailableSkips(t *testing.T) {
	t.Parallel()
	cb := memoryCodebase(t, map[string]string{"a.txt": "x"})
	rows := []types.GuardRow{
		{Name: "needs a tool", Kind: types.GuardGo},
		{Name: "broken", Kind: types.GuardGo},
	}
	gs, err := guard.NewFromConfig(rows, guard.Deps{Go: map[string]guard.GoGuard{
		"needs a tool": func(
			context.Context, interfaces.Codebase, *types.GuardRow, map[string]string,
		) ([]types.Finding, error) {
			return nil, apperr.New(apperr.CodeUnavailable, "terraform is not installed")
		},
		"broken": func(
			context.Context, interfaces.Codebase, *types.GuardRow, map[string]string,
		) ([]types.Finding, error) {
			return nil, apperr.New(apperr.CodeInternal, "boom")
		},
	}})
	require.NoError(t, err)
	v := gs[0].Run(t.Context(), cb)
	assert.Equal(t, types.StatusSkip, v.Status)
	assert.Contains(t, v.Detail, "terraform is not installed")
	assert.Equal(t, types.StatusFail, gs[1].Run(t.Context(), cb).Status)
}
