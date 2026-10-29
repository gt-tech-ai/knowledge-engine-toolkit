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

// TestServicesGuard_For tests a row repeated over its data: `for` returns the placeholder
// maps, crossed with each's expansions.
//
// Why this test is important:
//   - A check per policy set (and per set × overlay) iterates the expectations the row
//     carries; if the iteration came from a product expander instead, the sets would be
//     listed twice — in the expander's config and in the row's data.
//
// What it tests:
//   - `for` alone expands one guard run per returned map, its values (strings and
//     numbers) as placeholders in the name and the expression's vars.
//   - With each, every expansion is crossed with the maps `for` returns over that
//     expansion's vars, the map's keys added to (and overriding) the expansion's.
//   - A `for` that is not a list of maps fails the row; one that does not compile fails
//     building.
func TestServicesGuard_For(t *testing.T) {
	t.Parallel()
	var rows []types.GuardRow
	require.NoError(t, yaml.Unmarshal([]byte(`
- name: "{set} has port {port}"
  kind: expr
  data: &sets [{name: open, port: 8080}, {name: closed, port: 9090}]
  for: "data.map(s, {'set': s.name, 'port': s.port})"
  expr: "vars.port == '8080' ? [] : ['port ' + vars.port + ' of ' + vars.set]"
- name: "{env}/{set}"
  kind: expr
  each: envs
  data: *sets
  for: "data.filter(s, vars.env == 'prod' || s.name == 'open').map(s, {'set': s.name})"
  expr: "['ran ' + vars.env + '/' + vars.set]"
- name: bad for
  kind: expr
  data: 1
  for: data
  expr: "true"
`), &rows))
	each := func(context.Context, interfaces.Codebase) ([]map[string]string, error) {
		return []map[string]string{{"env": "dev"}, {"env": "prod"}}, nil
	}
	gs, err := guard.NewFromConfig(rows, guard.Deps{
		Expanders: map[string]guard.Expander{"envs": each},
	})
	require.NoError(t, err)
	cb := memoryCodebase(t, guardFiles())
	v := map[string]types.Verdict{}
	for _, g := range gs {
		v[g.Name()] = g.Run(t.Context(), cb)
	}

	assert.Equal(t, "port 9090 of closed\n", v["{set} has port {port}"].Detail)
	assert.Equal(
		t,
		"ran dev/open\nran prod/open\nran prod/closed\n",
		v["{env}/{set}"].Detail,
	)
	assert.Equal(t, types.StatusFail, v["bad for"].Status)
	assert.Contains(t, v["bad for"].Detail, "for")

	_, err = guard.NewFromConfig([]types.GuardRow{
		{Name: "x", Kind: types.GuardExpr, Expr: "true", For: "data.("},
	}, guard.Deps{})
	require.Error(t, err)
}
