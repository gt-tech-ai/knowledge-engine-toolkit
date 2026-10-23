package toolrun

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Select returns the runs sel matches (see types.Selection), in their order.
func Select(runs []types.ToolRun, sel types.Selection) []types.ToolRun {
	var out []types.ToolRun
	for i := range runs {
		if sel.Matches(runs[i].Name, runs[i].Verb, runs[i].Tags) {
			out = append(out, runs[i])
		}
	}
	return out
}
