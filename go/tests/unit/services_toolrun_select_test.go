package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/toolrun"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
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
		fixtures.Build(func(row *types.ToolRun) {
			row.Name = textGolangciLint
			row.Verb = textLint
			row.Tags = []string{textGoLower}
		}),
		fixtures.Build(func(row *types.ToolRun) {
			row.Name = textRuff
			row.Verb = textLint
			row.Tags = []string{textPython, textFast}
		}),
		fixtures.Build(func(row *types.ToolRun) {
			row.Name = textHadolint
			row.Verb = textLint
			row.Tags = []string{textInfra, textFast}
		}),
		fixtures.Build(func(row *types.ToolRun) {
			row.Name = textGofumpt
			row.Verb = textFmt
			row.Tags = []string{textGoLower, textFast}
		}),
	}
	names := func(rs []types.ToolRun) []string {
		out := make([]string, 0, len(rs))
		for _, r := range rs {
			out = append(out, r.Name)
		}

		return out
	}
	cases := testSelectTable()

	for name, tc := range cases {
		assert.Equal(t, tc.want, names(toolrun.Select(runs, &tc.sel)), name)
	}
}

// testSelectRow is one row of testSelectTable.
type testSelectRow struct {
	sel  types.Selection
	want []string
}

// testSelectTable builds the table TestSelect runs.
func testSelectTable() map[string]testSelectRow {
	return map[string]testSelectRow{
		textEmpty: {
			stdx.Zero[types.Selection](),
			[]string{textGolangciLint, textRuff, textHadolint, textGofumpt},
		},
		"verbs": {
			fixtures.Build(func(row *types.Selection) {
				row.Verbs = []string{textFmt}
			}),
			[]string{textGofumpt},
		},
		"every tag": {
			fixtures.Build(func(row *types.Selection) {
				row.Tags = []string{textGoLower, textFast}
			}),
			[]string{textGofumpt},
		},
		"any tag": {
			fixtures.Build(func(row *types.Selection) {
				row.AnyTags = []string{textPython, textInfra}
			}),
			[]string{textRuff, textHadolint},
		},
		"exclude tags": {
			fixtures.Build(func(row *types.Selection) {
				row.ExcludeTags = []string{textFast}
			}),
			[]string{textGolangciLint},
		},
		"names": {
			fixtures.Build(func(row *types.Selection) {
				row.Names = []string{textRuff, textGofumpt}
			}),
			[]string{textRuff, textGofumpt},
		},
		"exclude": {
			fixtures.Build(func(row *types.Selection) {
				row.Exclude = []string{textRuff}
			}),
			[]string{textGolangciLint, textHadolint, textGofumpt},
		},
		"intersection": {
			fixtures.Build(func(row *types.Selection) {
				row.Verbs = []string{textLint}
				row.AnyTags = []string{textGoLower, textFast}
				row.ExcludeTags = []string{textInfra}
			}),
			[]string{textGolangciLint, textRuff},
		},
	}
}
