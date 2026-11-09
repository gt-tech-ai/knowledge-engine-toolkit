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
	_, missing, err := files(ctx, cb, r)
	if err != nil || missing != nil {
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

		out = append(out, yamlDocFindings(d.Path, ms, r)...)
	}

	return out, nil
}

// yamlDocFindings reports a path that selects nothing, or each value off the expectation.
func yamlDocFindings(
	doc string,
	ms []types.YAMLMatch,
	r *types.GuardRow,
) []types.Finding {
	if len(ms) == 0 {
		return []types.Finding{
			{File: doc, Rule: keyPath, Message: doc + " has no " + r.Path},
		}
	}

	var out []types.Finding

	for _, m := range ms {
		if r.Expect != "" && m.Value != r.Expect {
			out = append(out, types.Finding{
				File:    doc,
				Line:    m.Line,
				Rule:    "expect",
				Message: r.Path + " is " + quote(m.Value) + ", want " + quote(r.Expect),
			})
		}
	}

	return out
}

// quote double-quotes s.
func quote(s string) string { return `"` + s + `"` }
