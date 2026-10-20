package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/yamlq"
)

// yamlqDoc is a document with nested mappings, a sequence and a repeated key.
const yamlqDoc = `kind: Deployment
metadata:
  name: api
spec:
  template:
    spec:
      containers:
        - name: api
          image: registry/api@sha256:abc
          env:
            - name: existingSecret
              value: db-creds
        - name: sidecar
          image: registry/proxy:1.2
`

// TestYamlq_Path tests the path query language over a parsed document.
//
// Why this test is important:
//   - Declarative checks select manifest fields by path; a query that misses a
//     wildcard element or reports the wrong line sends the reader to the wrong place
//     or passes a check that should fail.
//
// What it tests:
//   - `$`, `.key`, `[n]`, `[*]`, `.*` and `..key` select the expected scalars with
//     their 1-based lines; a path that matches nothing returns no matches; a malformed
//     path is CodeInvalidInput.
func TestYamlq_Path(t *testing.T) {
	t.Parallel()
	var root yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(yamlqDoc), &root))

	got, err := yamlq.Path(&root, "$.metadata.name")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "api", got[0].Value)
	assert.Equal(t, 3, got[0].Line)

	got, err = yamlq.Path(&root, "$.spec.template.spec.containers[*].image")
	require.NoError(t, err)
	assert.Equal(
		t,
		[]string{"registry/api@sha256:abc", "registry/proxy:1.2"},
		values(got),
	)

	got, err = yamlq.Path(&root, "$.spec.template.spec.containers[1].name")
	require.NoError(t, err)
	assert.Equal(t, []string{"sidecar"}, values(got))

	got, err = yamlq.Path(&root, "$..name")
	require.NoError(t, err)
	assert.Equal(t, []string{"api", "api", "existingSecret", "sidecar"}, values(got))

	got, err = yamlq.Path(&root, "$.metadata.*")
	require.NoError(t, err)
	assert.Equal(t, []string{"api"}, values(got))

	got, err = yamlq.Path(&root, "$")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "mapping", got[0].Kind)

	got, err = yamlq.Path(&root, "$.missing.key")
	require.NoError(t, err)
	assert.Empty(t, got)

	_, err = yamlq.Path(&root, "$.a[x]")
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	_, err = yamlq.Path(&root, "metadata")
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// values returns each match's value.
func values(ms []yamlq.Match) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Value)
	}
	return out
}

// yamlqEditDoc is two documents with head, line and foot comments.
const yamlqEditDoc = `# deployment rows
customers:
  acme:
    # the staging row
    region: us-east-1 # primary
    rows:
      - staging
# trailing note
---
other: true
`

// TestYamlq_UpsertAndDelete tests comment- and order-preserving edits.
//
// Why this test is important:
//   - Onboarding writes rows into hand-maintained YAML; an edit that drops comments,
//     reorders keys or rewrites the second document turns a one-line change into a
//     diff nobody can review.
//
// What it tests:
//   - Setting an existing scalar changes only that value; setting a missing nested key
//     appends it to its mapping; deleting a key removes only it; comments, key order and
//     the second document survive byte for byte; a wildcard path is CodeInvalidInput.
func TestYamlq_UpsertAndDelete(t *testing.T) {
	t.Parallel()
	out, err := yamlq.Upsert([]byte(yamlqEditDoc), "$.customers.acme.region", "eu-west-1")
	require.NoError(t, err)
	assert.Equal(t, `# deployment rows
customers:
  acme:
    # the staging row
    region: eu-west-1 # primary
    rows:
      - staging
# trailing note
---
other: true
`, string(out))

	out, err = yamlq.Upsert(
		[]byte(yamlqEditDoc),
		"$.customers.acme.apex.domain",
		"acme.example",
	)
	require.NoError(t, err)
	assert.Equal(t, `# deployment rows
customers:
  acme:
    # the staging row
    region: us-east-1 # primary
    rows:
      - staging
    apex:
      domain: acme.example
# trailing note
---
other: true
`, string(out))

	out, err = yamlq.Delete([]byte(yamlqEditDoc), "$.customers.acme.rows")
	require.NoError(t, err)
	assert.Equal(t, `# deployment rows
customers:
  acme:
    # the staging row
    region: us-east-1 # primary
# trailing note
---
other: true
`, string(out))

	out, err = yamlq.UpsertAt([]byte(yamlqEditDoc), 1, "$.other", false)
	require.NoError(t, err)
	assert.Contains(t, string(out), "---\nother: false\n")

	_, err = yamlq.Upsert([]byte(yamlqEditDoc), "$.customers.*.region", "x")
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}
