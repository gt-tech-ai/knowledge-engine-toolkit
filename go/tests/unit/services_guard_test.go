package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	clientcodebase "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/codebase"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/guard"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/suite"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// guardFiles returns the repository the guard tests run over.
func guardFiles() map[string]string {
	return map[string]string{
		textDeployAYaml:     "# image: latest\nimage: app:1.2\nreplicas: 2\n",
		textDeployBManifest: "image: app:latest\nreplicas: 1\n",
		textInfraMainTf:     "// TODO\nresource \"x\" {}\n",
		"notes/broken.yaml": "a: [\n",
		"policy/p.yaml": "kind: AuthorizationPolicy\nspec:\n  rules:\n" +
			"  - from:\n    - source:\n        principals: [kong, api]\n",
	}
}

// runGuards builds rows and runs each guard over the guard repository.
func runGuards(
	t *testing.T,
	rows []types.GuardRow,
	deps *guard.Deps,
) map[string]types.Verdict {
	t.Helper()

	guards, err := guard.NewFromConfig(rows, deps)
	require.NoError(t, err)

	cb := memoryCodebase(t, guardFiles())
	out := map[string]types.Verdict{}

	for _, g := range guards {
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

	deploy := fixtures.Build(func(row *types.FileSelector) {
		row.Globs = []string{"deploy/*.yaml"}
	})
	statusByName := runGuards(t, testServicesGuardMatchTable(&deploy), new(guard.Deps))

	require.Equal(t, types.StatusFail, statusByName["no-latest"].Status)
	require.Len(t, statusByName["no-latest"].Findings, 1)
	assert.Equal(t, textDeployBManifest, statusByName["no-latest"].Findings[0].File)
	assert.Equal(t, 1, statusByName["no-latest"].Findings[0].Line)
	assert.Contains(t, statusByName["no-latest"].Detail, "deploy/b.yaml:1:")
	assert.Equal(t, types.StatusPass, statusByName["has-image"].Status)
	require.Equal(t, types.StatusFail, statusByName["has-replicas-2"].Status)
	assert.Contains(
		t,
		statusByName["has-replicas-2"].Detail,
		`deploy/b.yaml does not contain "replicas: 2"`,
	)
	assert.Equal(t, types.StatusPass, statusByName["tf"].Status)
	assert.Equal(t, types.StatusFail, statusByName[textNone].Status)
	assert.Equal(t, types.StatusPass, statusByName["none-ok"].Status)
}

// testServicesGuardMatchTable builds the table TestServicesGuard_Match runs.
func testServicesGuardMatchTable(deploy *types.FileSelector) []types.GuardRow {
	return []types.GuardRow{
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = "no-latest"
			row.Kind = types.GuardMatch
			row.Files = *deploy
			row.SkipComments = true
			row.Forbid = []string{`:latest\b`}
		}),
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = textPinned
			row.Kind = types.GuardMatch
			row.Files = *deploy
			row.Literal = true
			row.Names = []string{"has-image", "has-replicas-2"}
			row.Require = []string{"image:", "replicas: 2"}
		}),
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = "tf"
			row.Kind = types.GuardMatch
			row.SkipComments = true
			row.Files = fixtures.Build(func(row *types.FileSelector) {
				row.Paths = []string{textInfraMainTf}
			})
			row.Forbid = []string{"TODO"}
		}),
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = textNone
			row.Kind = types.GuardMatch
			row.Files = fixtures.Build(func(row *types.FileSelector) {
				row.Paths = []string{textX}
			})
		}),
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = "none-ok"
			row.Kind = types.GuardMatch
			row.Optional = true
			row.Files = fixtures.Build(func(row *types.FileSelector) {
				row.Paths = []string{textX}
			})
		}),
	}
}

// TestServicesGuard_MatchAllow tests line exemptions on a match row's forbid patterns.
//
// Why this test is important:
//   - RE2 has no lookahead, so a forbid pattern that must spare a sanctioned literal
//     needs an allow list; without it the guard either misses leaks or flags the
//     sanctioned line, and an allow list that comes from data must expand.
//
// What it tests:
//   - a line matching an allow pattern is not a forbid finding, other lines still are;
//   - an allow pattern expands {placeholders} from an each expansion;
//   - an allow pattern that does not compile fails the build of the guards.
func TestServicesGuard_MatchAllow(t *testing.T) {
	t.Parallel()

	deploy := fixtures.Build(func(row *types.FileSelector) {
		row.Globs = []string{"deploy/*.yaml"}
	})
	each := func(context.Context, interfaces.Codebase) ([]map[string]string, error) {
		return []map[string]string{{textPinned: `app:latest`}}, nil
	}
	statusByName := runGuards(t, []types.GuardRow{
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = "no-app-tags"
			row.Kind = types.GuardMatch
			row.Files = deploy
			row.Forbid = []string{`image: app:`}
			row.Allow = []string{`app:1\.2$`}
		}),
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = "expanded"
			row.Kind = types.GuardMatch
			row.Files = deploy
			row.Each = "sanctioned"
			row.Forbid = []string{`image: app:`}
			row.Allow = []string{`app:1\.2$`, "{pinned}"}
		}),
	}, fixtures.BuildRef(func(row *guard.Deps) {
		row.Expanders = map[string]guard.Expander{"sanctioned": each}
	}))

	require.Equal(t, types.StatusFail, statusByName["no-app-tags"].Status)
	require.Len(t, statusByName["no-app-tags"].Findings, 1)
	assert.Equal(t, textDeployBManifest, statusByName["no-app-tags"].Findings[0].File)
	assert.Equal(t, types.StatusPass, statusByName["expanded"].Status)

	_, err := guard.NewFromConfig(
		[]types.GuardRow{fixtures.Build(func(row *types.GuardRow) {
			row.Name = textBad
			row.Kind = types.GuardMatch
			row.Files = deploy
			row.Allow = []string{textOpen}
		})},
		new(guard.Deps),
	)
	require.Error(t, err)
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
		return []map[string]string{
			{textF: textA, textN: "2"},
			{textF: textB, textN: "2"},
		}, nil
	}
	statusByName := runGuards(t, []types.GuardRow{
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = "replicas"
			row.Kind = types.GuardYAML
			row.Each = "deploys"
			row.Path = "$.replicas"
			row.Expect = "{n}"
			row.Files = fixtures.Build(func(row *types.FileSelector) {
				row.Paths = []string{"deploy/{f}.yaml"}
			})
		}),
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = textMissing
			row.Kind = types.GuardYAML
			row.Path = "$.nope"
			row.Files = fixtures.Build(func(row *types.FileSelector) {
				row.Paths = []string{textDeployAYaml}
			})
		}),
	}, fixtures.BuildRef(func(row *guard.Deps) {
		row.Expanders = map[string]guard.Expander{"deploys": each}
	}))

	require.Equal(t, types.StatusFail, statusByName["replicas"].Status)
	require.Len(t, statusByName["replicas"].Findings, 1)
	assert.Equal(t, textDeployBManifest, statusByName["replicas"].Findings[0].File)
	assert.Equal(t, wantLineEach, statusByName["replicas"].Findings[0].Line)
	assert.Equal(t, types.StatusFail, statusByName[textMissing].Status)
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

	policies := fixtures.Build(func(row *types.FileSelector) {
		row.Globs = []string{"policy/*.yaml"}
	})
	statusByName := runGuards(t, testServicesGuardExprTable(&policies), new(guard.Deps))

	require.Equal(t, types.StatusFail, statusByName["two-principals"].Status)
	assert.Equal(
		t,
		"too many principals",
		statusByName["two-principals"].Findings[0].Message,
	)
	assert.Equal(t, types.StatusPass, statusByName["listed"].Status)
	require.Equal(t, types.StatusFail, statusByName[textText].Status)
	assert.Equal(t, textDeployBManifest, statusByName[textText].Findings[0].File)
	assert.Equal(t, 1, statusByName[textText].Findings[0].Line)
	assert.Equal(
		t,
		types.StatusPass,
		statusByName["literal"].Status,
		statusByName["literal"].Detail,
	)
	assert.Equal(
		t,
		types.StatusPass,
		statusByName["bind"].Status,
		statusByName["bind"].Detail,
	)

	_, err := guard.NewFromConfig([]types.GuardRow{
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = textBad
			row.Kind = types.GuardExpr
			row.Expr = "files.("
		}),
	}, new(guard.Deps))
	require.Error(t, err)
}

// testServicesGuardExprTable builds the table TestServicesGuard_Expr runs.
func testServicesGuardExprTable(policies *types.FileSelector) []types.GuardRow {
	return []types.GuardRow{
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = "two-principals"
			row.Kind = types.GuardExpr
			row.Files = *policies
			row.Message = "too many principals"
			row.Expr = `docs.all(d, d.doc.spec.rules.all(r, r.from.all(f,
				size(f.source.principals) <= 1)))`
		}),
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = "listed"
			row.Kind = types.GuardExpr
			row.Files = *policies
			row.Expr = `docs.filter(d, !("kong" in d.doc.spec.rules[0].from[0].source.principals))
				.map(d, {"file": d.path, "line": 1, "message": "kong missing"})`
		}),
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = textText
			row.Kind = types.GuardExpr
			row.Files = fixtures.Build(func(row *types.FileSelector) {
				row.Globs = []string{textDeployGlob}
			})
			row.Expr = `files.filter(p, file[p].text.contains("latest") && p.endsWith("b.yaml"))
				.map(p, {"file": p, "line": 1, "message": "latest"})`
		}),
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = "bind"
			row.Kind = types.GuardExpr
			row.Files = fixtures.Build(func(row *types.FileSelector) {
				row.Globs = []string{textDeployGlob}
			})
			row.Expr = `cel.bind(t, file["deploy/a.yaml"].text,
				regex.extract(t, "image: (app:[0-9.]+)") == optional.of("app:1.2"))`
		}),
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = "literal"
			row.Kind = types.GuardExpr
			row.Files = fixtures.Build(func(row *types.FileSelector) {
				row.Globs = []string{"notes/*"}
			})
			row.Expr = `files.all(p, p != "docs/objects/file")`
		}),
	}
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
		return []types.Finding{
			fixtures.Build(func(target *types.Finding) {
				target.Message = row.Message + " " + vars[textEnv]
			}),
		}, nil
	}
	each := func(context.Context, interfaces.Codebase) ([]map[string]string, error) {
		return []map[string]string{{textEnv: textA}}, nil
	}
	guards, err := guard.NewFromConfig([]types.GuardRow{
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = textComputed
			row.Kind = types.GuardGo
			row.Verb = textCheck
			row.Tags = []string{textK8s}
			row.Each = textEnvs
			row.Message = "computed in {env}"
		}),
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = textShared
			row.Kind = types.GuardGo
			row.Func = textComputed
			row.Message = textShared
		}),
	}, fixtures.BuildRef(func(row *guard.Deps) {
		row.Go = map[string]guard.GoGuard{textComputed: computed}
		row.Expanders = map[string]guard.Expander{textEnvs: each}
	}))
	require.NoError(t, err)

	testServicesGuardGoAndValidationChecksEntries(t, guards)
}

// testServicesGuardGoAndValidationChecksEntries continues
// TestServicesGuard_GoAndValidation.
func testServicesGuardGoAndValidationChecksEntries(
	t *testing.T,
	guards []interfaces.Guard,
) {
	t.Helper()

	assert.Equal(t, types.EntryMeta{
		Name: textComputed, Verb: textCheck, Group: textCheck,
		Tags:  []string{textK8s},
		Tools: nil,
	}, guards[0].Meta())

	codebase, err := clientcodebase.NewFromConfig(
		t.Context(),
		stdx.Zero[clientcodebase.Config](),
		fixtures.BuildRef(func(row *clientcodebase.Deps) {
			row.Decorations = fixtures.Decorations()
		}),
	)
	require.NoError(t, err)

	entries := suite.Guards(guards[:1], codebase)
	require.Len(t, entries, 1)
	assert.Equal(t, suite.KindGuard, entries[0].Kind)

	res, err := entries[0].Job(fixtures.Build(func(row *suite.Env) {
		row.Decs = fixtures.Decorations()
	})).
		Execute(t.Context(), nil, "")
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.Equal(t, types.StatusFail, res[0].Status)
	assert.Equal(t, "computed in a a", res[0].Error)
	assert.Equal(t, "shared ", guards[1].Run(t.Context(), codebase).Findings[0].Message)

	invalidRows := testServicesGuardGoAndValidationTable()
	for index := range invalidRows {
		guardRow := &invalidRows[index]
		_, err := guard.NewFromConfig([]types.GuardRow{*guardRow}, new(guard.Deps))
		require.Error(t, err, guardRow.Name)
	}
}

// testServicesGuardGoAndValidationTable builds the table
// TestServicesGuard_GoAndValidation runs.
func testServicesGuardGoAndValidationTable() []types.GuardRow {
	return []types.GuardRow{
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = textK
			row.Kind = textRegex
		}),
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = textE
			row.Kind = types.GuardMatch
			row.Each = textNope
		}),
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = "g"
			row.Kind = types.GuardGo
		}),
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = "y"
			row.Kind = types.GuardYAML
		}),
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = textN
			row.Kind = types.GuardMatch
			row.Names = []string{textA}
		}),
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

	renderSpec := fixtures.Build(func(row *types.RenderSpec) {
		row.ChartDir = textChart
		row.Set = map[string]string{textKind: "hpa"}
	})
	bad := fixtures.Build(func(row *types.RenderSpec) {
		row.ChartDir = textChart
		row.Set = map[string]string{textKind: textX}
	})
	renderer := mocks.NewMockRenderer(gomock.NewController(t))
	renderer.EXPECT().Render(gomock.Any(), any(&renderSpec)).Return([]types.Object{
		fixtures.Build(func(row *types.Object) {
			row.Kind = "HorizontalPodAutoscaler"
			row.Doc = map[string]any{textKind: "HorizontalPodAutoscaler"}
		}),
	}, nil).AnyTimes()
	renderer.EXPECT().Render(gomock.Any(), any(&bad)).
		Return(nil, apperr.New(apperr.CodeUpstream, "helm template: unknown autoscaling.kind")).
		AnyTimes()

	codebase, err := clientcodebase.NewFromConfig(
		t.Context(),
		stdx.Zero[clientcodebase.Config](),
		fixtures.BuildRef(func(row *clientcodebase.Deps) {
			row.Decorations = fixtures.Decorations()
			row.Renderer = renderer
		}),
	)
	require.NoError(t, err)

	guards, err := guard.NewFromConfig([]types.GuardRow{
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = "one-hpa"
			row.Kind = types.GuardExpr
			row.Render = renderSpec
			row.Expr = `objects.filter(o, o.kind == 'HorizontalPodAutoscaler').size() == 1`
		}),
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = "render-fails"
			row.Kind = types.GuardExpr
			row.Render = bad
			row.Expr = `objects.size() == 0`
		}),
	}, new(guard.Deps))
	require.NoError(t, err)

	statusByName := map[string]types.Verdict{}
	for _, g := range guards {
		statusByName[g.Name()] = g.Run(t.Context(), codebase)
	}

	assert.Equal(
		t,
		types.StatusPass,
		statusByName["one-hpa"].Status,
		statusByName["one-hpa"].Detail,
	)
	assert.Equal(t, types.StatusFail, statusByName["render-fails"].Status)
	assert.Contains(t, statusByName["render-fails"].Detail, "unknown autoscaling.kind")
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

	codebase := memoryCodebase(t, map[string]string{
		"tf/main.tf": "resource \"aws_kms_key\" \"cosign\" {\n" +
			"  key_usage = \"SIGN_VERIFY\"\n}\n",
		textObsValuesYaml: "alloy:\n  content: |\n    loki.process \"redact\" {\n" +
			"      forward_to = [loki.write.default.receiver]\n    }\n",
	})
	guards, err := guard.NewFromConfig([]types.GuardRow{
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = "hcl"
			row.Kind = types.GuardExpr
			row.Files = fixtures.Build(func(row *types.FileSelector) {
				row.Paths = []string{"tf/main.tf"}
				row.Exts = []string{textTf}
			})
			row.Expr = `docs.size() == 1 && docs[0].format == 'hcl' &&
				docs[0].doc.resource.aws_kms_key.cosign[0].key_usage == 'SIGN_VERIFY'`
		}),
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = "embedded"
			row.Kind = types.GuardExpr
			row.Files = fixtures.Build(func(row *types.FileSelector) {
				row.Paths = []string{textObsValuesYaml}
			})
			row.Expr = `decode('alloy', docs[0].doc.alloy.content)['loki.process']['redact'][0]
				.forward_to == ['loki.write.default.receiver']`
		}),
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = textUnknown
			row.Kind = types.GuardExpr
			row.Files = fixtures.Build(func(row *types.FileSelector) {
				row.Paths = []string{textObsValuesYaml}
			})
			row.Expr = `decode('ini', docs[0].doc.alloy.content) != null`
		}),
	}, new(guard.Deps))
	require.NoError(t, err)

	statusByName := map[string]types.Verdict{}
	for _, g := range guards {
		statusByName[g.Name()] = g.Run(t.Context(), codebase)
	}

	assert.Equal(
		t,
		types.StatusPass,
		statusByName["hcl"].Status,
		statusByName["hcl"].Detail,
	)
	assert.Equal(
		t,
		types.StatusPass,
		statusByName["embedded"].Status,
		statusByName["embedded"].Detail,
	)
	assert.Equal(t, types.StatusFail, statusByName[textUnknown].Status)
	assert.Contains(t, statusByName[textUnknown].Detail, "unknown format ini")
}

// TestServicesGuard_ExprSHA256 tests sha256(text) over a selected file's text.
//
// Why this test is important:
//   - a file copied from another repository (shared golden fixtures, a vendored schema)
//     must stay byte-identical to its source; recording the source's digests as row data
//     lets an expression catch a local edit or a stale copy without a Go guard.
//
// What it tests:
//   - sha256 returns the lowercase-hex SHA-256 of a file's text, so a recorded digest
//     passes and the digest of different bytes fails.
func TestServicesGuard_ExprSHA256(t *testing.T) {
	t.Parallel()

	codebase := memoryCodebase(t, map[string]string{"golden/case.json": "abc"})
	sel := fixtures.Build(func(row *types.FileSelector) {
		row.Paths = []string{"golden/case.json"}
	})
	guards, err := guard.NewFromConfig([]types.GuardRow{
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = "match"
			row.Kind = types.GuardExpr
			row.Files = sel
			row.Expr = `sha256(file['golden/case.json'].text) ==
				'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad'`
		}),
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = "differs"
			row.Kind = types.GuardExpr
			row.Files = sel
			row.Expr = `sha256(file['golden/case.json'].text) == sha256('abd')`
		}),
	}, new(guard.Deps))
	require.NoError(t, err)

	statusByName := map[string]types.Verdict{}
	for _, g := range guards {
		statusByName[g.Name()] = g.Run(t.Context(), codebase)
	}

	assert.Equal(
		t,
		types.StatusPass,
		statusByName["match"].Status,
		statusByName["match"].Detail,
	)
	assert.Equal(t, types.StatusFail, statusByName["differs"].Status)
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

	codebase := memoryCodebase(t, map[string]string{
		"osv.toml": "[[IgnoredVulns]]\nid = \"GHSA-1\"\n" +
			"ignoreUntil = 2026-06-01T00:00:00Z\n",
		"cfg.yaml": "embedded: |\n  until = 2026-01-15\n",
	})
	rows := []types.GuardRow{
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = "unexpired"
			row.Kind = types.GuardExpr
			row.Files = fixtures.Build(func(row *types.FileSelector) {
				row.Paths = []string{"osv.toml"}
				row.Exts = []string{".toml"}
			})
			row.Expr = `docs.size() == 1 && docs[0].format == 'toml' &&
				docs[0].doc.IgnoredVulns.all(e, e.ignoreUntil > now)`
		}),
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = "embedded"
			row.Kind = types.GuardExpr
			row.Files = fixtures.Build(func(row *types.FileSelector) {
				row.Paths = []string{"cfg.yaml"}
			})
			row.Expr = `decode('toml', docs[0].doc.embedded).until < now`
		}),
	}

	testServicesGuardExprTOMLAndNowChecksVerdicts(t, codebase, rows)
}

// testServicesGuardExprTOMLAndNowChecksVerdicts continues
// TestServicesGuard_ExprTOMLAndNow.
func testServicesGuardExprTOMLAndNowChecksVerdicts(
	t *testing.T,
	codebase interfaces.Codebase,
	rows []types.GuardRow,
) {
	t.Helper()

	run := func(now time.Time) map[string]types.Verdict {
		guards, err := guard.NewFromConfig(
			rows,
			fixtures.BuildRef(func(row *guard.Deps) {
				row.Now = func() time.Time { return now }
			}),
		)
		require.NoError(t, err)

		vs := map[string]types.Verdict{}
		for _, g := range guards {
			vs[g.Name()] = g.Run(t.Context(), codebase)
		}

		return vs
	}

	before := run(time.Date(dateInput, dateInputNow, 1, 0, 0, 0, 0, time.UTC))
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

	after := run(time.Date(dateInput, dateInputTOMLAndNow, 1, 0, 0, 0, 0, time.UTC))
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

	codebase := memoryCodebase(t, map[string]string{"a.txt": textX})
	rows := []types.GuardRow{
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = "needs a tool"
			row.Kind = types.GuardGo
		}),
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = textBroken
			row.Kind = types.GuardGo
		}),
	}
	guards, err := guard.NewFromConfig(rows, fixtures.BuildRef(func(row *guard.Deps) {
		row.Go = map[string]guard.GoGuard{
			"needs a tool": func(
				context.Context, interfaces.Codebase, *types.GuardRow, map[string]string,
			) ([]types.Finding, error) {
				return nil, apperr.New(
					apperr.CodeUnavailable,
					"terraform is not installed",
				)
			},
			textBroken: func(
				context.Context, interfaces.Codebase, *types.GuardRow, map[string]string,
			) ([]types.Finding, error) {
				return nil, apperr.New(apperr.CodeInternal, textBoom)
			},
		}
	}))
	require.NoError(t, err)

	v := guards[0].Run(t.Context(), codebase)
	assert.Equal(t, types.StatusSkip, v.Status)
	assert.Contains(t, v.Detail, "terraform is not installed")
	assert.Equal(t, types.StatusFail, guards[1].Run(t.Context(), codebase).Status)
}

const (
	// dateInputNow is the date input now the tests use.
	dateInputNow = 3

	// dateInputTOMLAndNow is the date input tomland now the tests use.
	dateInputTOMLAndNow = 7

	// wantLineEach is the expected line each.
	wantLineEach = 2
)

const (
	// textObsValuesYaml is the literal "obs/values.yaml".
	textObsValuesYaml = "obs/values.yaml"

	// textComputed is the literal "computed".
	textComputed = "computed"
)
