package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/toolrun"
)

// TestSelect tests how a suite picks its rows.
//
// Why this test is important:
//   - A suite is a selection over every row in the manifest; a rule that drops or adds a
//     row changes what a command checks without anyone editing that command.
//
// What it tests:
//   - Each selection field filters on its own (verbs, every tag, any tag, excluded
//     tags, names, excluded names), the fields intersect, an empty selection keeps every
//     row, and the result keeps the rows' order.
func TestSelect(t *testing.T) {
	t.Parallel()
	runs := []types.ToolRun{
		{Name: "golangci-lint", Verb: "lint", Tags: []string{"go"}},
		{Name: "ruff", Verb: "lint", Tags: []string{"python", "fast"}},
		{Name: "hadolint", Verb: "lint", Tags: []string{"infra", "fast"}},
		{Name: "gofumpt", Verb: "fmt", Tags: []string{"go", "fast"}},
	}
	names := func(rs []types.ToolRun) []string {
		out := make([]string, 0, len(rs))
		for _, r := range rs {
			out = append(out, r.Name)
		}
		return out
	}
	cases := map[string]struct {
		sel  types.Selection
		want []string
	}{
		"empty": {
			types.Selection{},
			[]string{"golangci-lint", "ruff", "hadolint", "gofumpt"},
		},
		"verbs": {types.Selection{Verbs: []string{"fmt"}}, []string{"gofumpt"}},
		"every tag": {
			types.Selection{Tags: []string{"go", "fast"}},
			[]string{"gofumpt"},
		},
		"any tag": {
			types.Selection{AnyTags: []string{"python", "infra"}},
			[]string{"ruff", "hadolint"},
		},
		"exclude tags": {
			types.Selection{ExcludeTags: []string{"fast"}},
			[]string{"golangci-lint"},
		},
		"names": {
			types.Selection{Names: []string{"ruff", "gofumpt"}},
			[]string{"ruff", "gofumpt"},
		},
		"exclude": {
			types.Selection{Exclude: []string{"ruff"}},
			[]string{"golangci-lint", "hadolint", "gofumpt"},
		},
		"intersection": {
			types.Selection{
				Verbs: []string{"lint"}, AnyTags: []string{"go", "fast"},
				ExcludeTags: []string{"infra"},
			},
			[]string{"golangci-lint", "ruff"},
		},
	}
	for name, tc := range cases {
		assert.Equal(t, tc.want, names(toolrun.Select(runs, tc.sel)), name)
	}
}
