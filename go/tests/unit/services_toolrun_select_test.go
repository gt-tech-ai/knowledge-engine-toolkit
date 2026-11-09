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
		{Name: textGolangciLint, Verb: textLint, Tags: []string{textGo2}},
		{Name: textRuff, Verb: textLint, Tags: []string{textPython, textFast}},
		{Name: textHadolint, Verb: textLint, Tags: []string{textInfra, textFast}},
		{Name: textGofumpt, Verb: textFmt, Tags: []string{textGo2, textFast}},
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
		textEmpty: {
			types.Selection{},
			[]string{textGolangciLint, textRuff, textHadolint, textGofumpt},
		},
		"verbs": {types.Selection{Verbs: []string{textFmt}}, []string{textGofumpt}},
		"every tag": {
			types.Selection{Tags: []string{textGo2, textFast}},
			[]string{textGofumpt},
		},
		"any tag": {
			types.Selection{AnyTags: []string{textPython, textInfra}},
			[]string{textRuff, textHadolint},
		},
		"exclude tags": {
			types.Selection{ExcludeTags: []string{textFast}},
			[]string{textGolangciLint},
		},
		"names": {
			types.Selection{Names: []string{textRuff, textGofumpt}},
			[]string{textRuff, textGofumpt},
		},
		"exclude": {
			types.Selection{Exclude: []string{textRuff}},
			[]string{textGolangciLint, textHadolint, textGofumpt},
		},
		"intersection": {
			types.Selection{
				Verbs: []string{textLint}, AnyTags: []string{textGo2, textFast},
				ExcludeTags: []string{textInfra},
			},
			[]string{textGolangciLint, textRuff},
		},
	}

	for name, tc := range cases {
		assert.Equal(t, tc.want, names(toolrun.Select(runs, tc.sel)), name)
	}
}
