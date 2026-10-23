package toolrun

import (
	"slices"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Select returns the runs sel matches (see types.Selection), in their order.
func Select(runs []types.ToolRun, sel types.Selection) []types.ToolRun {
	var out []types.ToolRun
	for i := range runs {
		if matches(&runs[i], sel) {
			out = append(out, runs[i])
		}
	}
	return out
}

// matches reports whether r satisfies every constraint sel sets.
func matches(r *types.ToolRun, sel types.Selection) bool {
	has := func(tag string) bool { return slices.Contains(r.Tags, tag) }
	switch {
	case len(sel.Verbs) > 0 && !slices.Contains(sel.Verbs, r.Verb),
		len(sel.Names) > 0 && !slices.Contains(sel.Names, r.Name),
		slices.Contains(sel.Exclude, r.Name),
		!allOf(sel.Tags, has),
		len(sel.AnyTags) > 0 && !slices.ContainsFunc(sel.AnyTags, has),
		slices.ContainsFunc(sel.ExcludeTags, has):
		return false
	}
	return true
}

// allOf reports whether every tag satisfies has.
func allOf(tags []string, has func(string) bool) bool {
	for _, t := range tags {
		if !has(t) {
			return false
		}
	}
	return true
}
