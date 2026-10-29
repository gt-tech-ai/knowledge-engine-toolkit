package guard

import (
	"context"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/gen"
)

// genCheck renders the generator r names and compares it with cb: one finding per
// output that is missing or out of date, its state followed by r's message. A row whose
// files select nothing checks nothing (it passes when optional), so a tree without the
// generator's source is not reported as drift.
func genCheck(
	ctx context.Context,
	cb interfaces.Codebase,
	r *types.GuardRow,
	lookup func(context.Context, string) (interfaces.Generator, error),
) ([]types.Finding, error) {
	if !r.Files.IsZero() {
		fs, none, err := files(ctx, cb, r)
		if err != nil || len(fs) == 0 {
			return none, err
		}
	}
	g, err := lookup(ctx, r.Gen)
	if err != nil {
		return nil, err
	}
	d, err := gen.Check(ctx, cb, g)
	if err != nil {
		return nil, err
	}
	out := make([]types.Finding, 0, len(d.Paths))
	for _, p := range d.Paths {
		state := "out of date"
		if _, err := cb.Text(ctx, p); apperr.Is(err, apperr.CodeNotFound) {
			state = "missing"
		}
		if r.Message != "" {
			state += "; " + r.Message
		}
		out = append(out, types.Finding{File: p, Message: state})
	}
	return out, nil
}
