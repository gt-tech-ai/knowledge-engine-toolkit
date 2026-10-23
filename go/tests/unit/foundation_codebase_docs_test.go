package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
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
	cb := memoryCodebase(t, map[string]string{
		"infra/main.tf": "resource \"aws_kms_alias\" \"cosign\" {\n" +
			"  name          = \"alias/cosign\"\n" +
			"  target_key_id = aws_kms_key.cosign.key_id\n" +
			"  count         = 1 + 1\n" +
			"  tags          = { Name = \"x\" }\n" +
			"  ingress {\n    from_port = 443\n  }\n" +
			"  ingress {\n    from_port = 80\n  }\n" +
			"}\n" +
			"variable \"region\" {\n  default = \"us-east-1\"\n}\n",
		"obs/config.alloy": "loki.source.kubernetes \"pods\" {\n" +
			"  forward_to = [loki.process.redact.receiver]\n" +
			"}\n" +
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
			"}\n",
		"app/values.yaml": "a: 1\n---\nb: [x, y]\n",
		"bad/broken.tf":   "resource {\n",
	})
	ctx := t.Context()

	yamlOnly, err := cb.Docs(ctx, types.FileSelector{})
	require.NoError(t, err)
	require.Len(t, yamlOnly, 2)
	assert.Equal(t, "app/values.yaml", yamlOnly[1].Path)
	assert.Equal(t, 1, yamlOnly[1].Index)
	assert.Equal(t, "yaml", yamlOnly[1].Format)
	assert.Equal(t, map[string]any{"b": []any{"x", "y"}}, yamlOnly[1].Value)

	tf, err := cb.Docs(
		ctx,
		types.FileSelector{Paths: []string{"infra/main.tf"}, Exts: []string{".tf"}},
	)
	require.NoError(t, err)
	require.Len(t, tf, 1)
	assert.Equal(t, "hcl", tf[0].Format)
	doc, ok := tf[0].Value.(map[string]any)
	require.True(t, ok)
	aliases := doc["resource"].(map[string]any)["aws_kms_alias"].(map[string]any)
	alias := aliases["cosign"].([]any)[0].(map[string]any)
	assert.Equal(t, "alias/cosign", alias["name"])
	assert.Equal(t, "${aws_kms_key.cosign.key_id}", alias["target_key_id"])
	assert.InDelta(t, float64(2), alias["count"], 0,
		"a literal-only expression is evaluated")
	assert.Equal(t, map[string]any{"Name": "x"}, alias["tags"])
	assert.Len(t, alias["ingress"], 2, "repeated unlabeled blocks are one list")
	assert.Equal(t, "us-east-1",
		doc["variable"].(map[string]any)["region"].([]any)[0].(map[string]any)["default"])

	alloy, err := cb.Docs(ctx, types.FileSelector{Exts: []string{".alloy"}})
	require.NoError(t, err)
	require.Len(t, alloy, 1)
	assert.Equal(t, "alloy", alloy[0].Format)
	adoc := alloy[0].Value.(map[string]any)
	sources := adoc["loki.source.kubernetes"].(map[string]any)
	source := sources["pods"].([]any)[0].(map[string]any)
	assert.Equal(t, []any{"loki.process.redact.receiver"}, source["forward_to"],
		"a reference is its source text")
	proc := adoc["loki.process"].(map[string]any)["redact"].([]any)[0].(map[string]any)
	stages := proc["stage.replace"].([]any)
	require.Len(t, stages, 2)
	assert.Equal(t, `(?i)(bearer )\S+`, stages[0].(map[string]any)["expression"])
	assert.Equal(t, "${1}REDACTED", stages[0].(map[string]any)["replace"])
	assert.Equal(t, int64(2), stages[1].(map[string]any)["max"])
	assert.Equal(t, true, stages[1].(map[string]any)["keep"])

	_, err = cb.Docs(
		ctx,
		types.FileSelector{Paths: []string{"bad/broken.tf"}, Exts: []string{".tf"}},
	)
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.Contains(t, err.Error(), "bad/broken.tf")
}
