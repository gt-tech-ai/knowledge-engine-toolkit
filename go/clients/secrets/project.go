package secrets

import (
	"context"
	"sort"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// ProjectEnv resolves each field of ref through src and returns them as child-process
// environment entries, `VAR=value`, where fields maps a field name to its variable. It
// is where a secret value is revealed, for a child's environment only. A field src
// reports CodeNotFound is skipped, so the fields that resolve are still projected and
// the child fails on the specific missing variable; any other error is returned. The
// entries are sorted by variable.
func ProjectEnv(
	ctx context.Context,
	src interfaces.Source,
	ref types.Ref,
	fields map[string]string,
) ([]string, error) {
	block := make([]string, 0, len(fields))
	for field, envVar := range fields {
		r := ref
		r.Field = field
		secret, err := src.Get(ctx, r)
		if apperr.Is(err, apperr.CodeNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		block = append(block, envVar+"="+secret.Reveal())
	}
	sort.Strings(block)
	return block, nil
}
