package guard

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// yamlCheck queries r.Path in every document of every selected file: a document with no
// node at the path is a finding, and so is a node whose value is not r.Expect when set.
func yamlCheck(
	ctx context.Context,
	cb interfaces.Codebase,
	r *types.GuardRow,
) ([]types.Finding, error) {
	if _, missing, err := files(ctx, cb, r); err != nil || missing != nil {
		return missing, err
	}
	docs, err := cb.YAML().Docs(ctx, r.Files)
	if err != nil {
		return nil, err
	}
	var out []types.Finding
	for _, d := range docs {
		ms, err := cb.YAML().Query(ctx, d, r.Path)
		if err != nil {
			return nil, err
		}
		if len(ms) == 0 {
			out = append(out, types.Finding{
				File: d.Path, Rule: keyPath, Message: d.Path + " has no " + r.Path,
			})
			continue
		}
		for _, m := range ms {
			if r.Expect != "" && m.Value != r.Expect {
				out = append(out, types.Finding{
					File: d.Path,
					Line: m.Line,
					Rule: "expect",
					Message: r.Path + " is " + quote(
						m.Value,
					) + ", want " + quote(
						r.Expect,
					),
				})
			}
		}
	}
	return out, nil
}

// quote double-quotes s.
func quote(s string) string { return `"` + s + `"` }
