package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// TestFoundationCodebase_Docs tests the format-aware document view.
//
// Why this test is important:
//   - repository guards read Terraform, Alloy and YAML configuration as data; a reader
//     that hand-scans braces misses heredocs, interpolations and comments, so the view
//     decodes each format with its own parser into one JSON-like shape.
//
// What it tests:
//   - with no exts the view keeps YAML files only (the pre-existing behaviour);
//   - a .tf file decodes as Terraform's JSON syntax: blocks nested by type and labels,
//     each a list of bodies, literal values typed, other expressions as source text;
//   - a .alloy file decodes the same way, with dotted block names and the label nested;
//   - a file that does not parse is CodeInvalidInput naming it.
func TestFoundationCodebase_Docs(t *testing.T) {
	t.Parallel()

	codebase := memoryCodebase(t, testFoundationCodebaseDocsTable())
	ctx := t.Context()

	yamlOnly, err := codebase.Docs(ctx, new(types.FileSelector))
	require.NoError(t, err)
	require.Len(t, yamlOnly, wantYamlOnlyLen)
	assert.Equal(t, "app/values.yaml", yamlOnly[1].Path)
	assert.Equal(t, 1, yamlOnly[1].Index)
	assert.Equal(t, "yaml", yamlOnly[1].Format)
	assert.Equal(t, map[string]any{textB: []any{textX, "y"}}, yamlOnly[1].Value)

	documents, err := codebase.Docs(
		ctx,
		fixtures.BuildRef(func(row *types.FileSelector) {
			row.Paths = []string{textInfraMainTf}
			row.Exts = []string{textTf}
		}),
	)
	require.NoError(t, err)
	require.Len(t, documents, 1)
	assert.Equal(t, "hcl", documents[0].Format)

	doc, ok := documents[0].Value.(map[string]any)
	require.True(t, ok)

	aliases := asType[map[string]any](
		t,
		asType[map[string]any](t, doc["resource"])["aws_kms_alias"],
	)
	alias := asType[map[string]any](t, asType[[]any](t, aliases["cosign"])[0])
	assert.Equal(t, "alias/cosign", alias[textName])
	assert.Equal(t, "${aws_kms_key.cosign.key_id}", alias["target_key_id"])
	assert.InDelta(t, float64(wantAlias), alias[textCount], 0,
		"a literal-only expression is evaluated")
	assert.Equal(t, map[string]any{textNameTitle: textX}, alias[textTags])
	assert.Len(
		t,
		alias["ingress"],
		wantAliasLen,
		"repeated unlabeled blocks are one list",
	)

	region := asType[map[string]any](t, asType[[]any](t,
		asType[map[string]any](t, doc["variable"])["region"])[0])

	testFoundationCodebaseDocsAlloyAndFormats(ctx, t, codebase, region)
}

// testFoundationCodebaseDocsAlloyAndFormats continues TestFoundationCodebase_Docs: alloy
// and formats.
func testFoundationCodebaseDocsAlloyAndFormats(
	ctx context.Context,
	t *testing.T,
	codebase interfaces.Codebase,
	region map[string]any,
) {
	t.Helper()

	var err error

	assert.Equal(
		t,
		textUsEast1,
		region["default"],
	)

	alloy, err := codebase.Docs(
		ctx,
		fixtures.BuildRef(func(row *types.FileSelector) {
			row.Exts = []string{".alloy"}
		}),
	)
	require.NoError(t, err)
	require.Len(t, alloy, 1)
	assert.Equal(t, "alloy", alloy[0].Format)

	adoc := asType[map[string]any](t, alloy[0].Value)
	sources := asType[map[string]any](t, adoc["loki.source.kubernetes"])
	source := asType[map[string]any](t, asType[[]any](t, sources[textPods])[0])
	assert.Equal(t, []any{"loki.process.redact.receiver"}, source["forward_to"],
		"a reference is its source text")

	proc := asType[map[string]any](
		t,
		asType[[]any](t, asType[map[string]any](t, adoc["loki.process"])["redact"])[0],
	)
	stages := asType[[]any](t, proc["stage.replace"])
	require.Len(t, stages, wantStagesLen)
	assert.Equal(
		t,
		`(?i)(bearer )\S+`,
		asType[map[string]any](t, stages[0])["expression"],
	)
	assert.Equal(t, "${1}REDACTED", asType[map[string]any](t, stages[0])["replace"])
	assert.Equal(t, int64(wantAsType), asType[map[string]any](t, stages[1])[textMax])
	assert.Equal(t, true, asType[map[string]any](t, stages[1])["keep"])

	_, err = codebase.Docs(
		ctx,
		fixtures.BuildRef(func(row *types.FileSelector) {
			row.Paths = []string{"bad/broken.tf"}
			row.Exts = []string{textTf}
		}),
	)
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.Contains(t, err.Error(), "bad/broken.tf")
}

// testFoundationCodebaseDocsTable builds the table TestFoundationCodebase_Docs runs.
func testFoundationCodebaseDocsTable() map[string]string {
	return map[string]string{
		textInfraMainTf: "resource \"aws_kms_alias\" \"cosign\" {\n" +
			"  name          = \"alias/cosign\"\n" +
			"  target_key_id = aws_kms_key.cosign.key_id\n" +
			"  count         = 1 + 1\n" +
			"  tags          = { Name = \"x\" }\n" +
			"  ingress {\n    from_port = 443\n  }\n" +
			"  ingress {\n    from_port = 80\n  }\n" +
			textBraceCloseNewline +
			"variable \"region\" {\n  default = \"us-east-1\"\n}\n",
		"obs/config.alloy": "loki.source.kubernetes \"pods\" {\n" +
			"  forward_to = [loki.process.redact.receiver]\n" +
			textBraceCloseNewline +
			"loki.process \"redact\" {\n" +
			"  forward_to = [loki.write.default.receiver]\n" +
			"  stage.replace {\n" +
			"    expression = \"(?i)(bearer )\\\\S+\"\n" +
			"    replace    = \"${1}REDACTED\"\n" +
			"  }\n" +
			"  stage.replace {\n" +
			"    expression = \"token=\\\\S+\"\n" +
			"    replace    = \"token=REDACTED\"\n" +
			"    max        = 2\n" +
			"    keep       = true\n" +
			"  }\n" +
			textBraceCloseNewline,
		"app/values.yaml": "a: 1\n---\nb: [x, y]\n",
		"bad/broken.tf":   "resource {\n",
	}
}

const (
	// wantAlias is the expected alias.
	wantAlias = 2

	// wantAliasLen is the expected alias len.
	wantAliasLen = 2

	// wantAsType is the expected as type.
	wantAsType = 2

	// wantStagesLen is the expected stages len.
	wantStagesLen = 2

	// wantYamlOnlyLen is the expected yaml only len.
	wantYamlOnlyLen = 2
)
