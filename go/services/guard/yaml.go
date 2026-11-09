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
	codebase interfaces.Codebase,
	guardRow *types.GuardRow,
) ([]types.Finding, error) {
	_, missing, err := files(ctx, codebase, guardRow)
	if err != nil || missing != nil {
		return missing, err
	}

	docs, err := codebase.YAML().Docs(ctx, &guardRow.Files)
	if err != nil {
		return nil, err
	}

	var out []types.Finding

	for _, yamlDoc := range docs {
		ms, err := codebase.YAML().Query(ctx, yamlDoc, guardRow.Path)
		if err != nil {
			return nil, err
		}

		out = append(out, yamlDocFindings(yamlDoc.Path, ms, guardRow)...)
	}

	return out, nil
}

// yamlDocFindings reports a path that selects nothing, or each value off the expectation.
func yamlDocFindings(
	doc string,
	yamlMatchs []types.YAMLMatch,
	guardRow *types.GuardRow,
) []types.Finding {
	if len(yamlMatchs) == 0 {
		return []types.Finding{
			{File: doc, Rule: keyPath, Message: doc + " has no " + guardRow.Path},
		}
	}

	var out []types.Finding

	for _, yamlMatch := range yamlMatchs {
		if guardRow.Expect != "" && yamlMatch.Value != guardRow.Expect {
			out = append(out, types.Finding{
				File: doc,
				Line: yamlMatch.Line,
				Rule: "expect",
				Message: guardRow.Path + " is " + quote(
					yamlMatch.Value,
				) + ", want " + quote(
					guardRow.Expect,
				),
			})
		}
	}

	return out
}

// quote double-quotes s.
func quote(s string) string { return `"` + s + `"` }
