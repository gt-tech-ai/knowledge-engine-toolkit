package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/guard"
)

// TestServicesGuard_ExprData tests an expr row's data: structured expectations read as
// `data`.
//
// Why this test is important:
//   - A policy check compares the tree with structured expectations (the callers each
//     port admits, the exact policy set per component); carried as row data, decoded
//     from the guard file, they need no product config section and no Go guard.
//
// What it tests:
//   - A row's data, decoded from YAML, is the `data` variable, indexed by the each
//     expansion's vars: the expected set passes, and a principal the data does not
//     admit fails naming its file.
//   - A row with no data sees `data` as null.
func TestServicesGuard_ExprData(t *testing.T) {
	t.Parallel()
	var rows []types.GuardRow
	require.NoError(t, yaml.Unmarshal([]byte(`
- name: "{set} admits only its callers"
  kind: expr
  each: sets
  files: {globs: [policy/*.yaml]}
  data:
    open: {callers: [kong, api]}
    closed: {callers: [kong]}
  expr: >-
    docs.map(d, d.doc.spec.rules.map(r, r.from.map(f, f.source.principals)))
      .flatten().flatten().flatten()
      .filter(p, !(p in data[vars.set].callers))
      .map(p, {'file': 'policy/p.yaml', 'message': p + ' is not admitted'})
- name: no data
  kind: expr
  expr: data == null
`), &rows))
	each := func(context.Context, interfaces.Codebase) ([]map[string]string, error) {
		return []map[string]string{{"set": "open"}, {"set": "closed"}}, nil
	}
	gs, err := guard.NewFromConfig(rows, guard.Deps{
		Expanders: map[string]guard.Expander{"sets": each},
	})
	require.NoError(t, err)
	cb := memoryCodebase(t, guardFiles())
	v := map[string]types.Verdict{}
	for _, g := range gs {
		v[g.Name()] = g.Run(t.Context(), cb)
	}

	set := v["{set} admits only its callers"]
	require.Equal(t, types.StatusFail, set.Status, set.Detail)
	assert.Equal(t, "policy/p.yaml: api is not admitted\n", set.Detail)
	assert.Equal(t, types.StatusPass, v["no data"].Status, v["no data"].Detail)
}
