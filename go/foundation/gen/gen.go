// Package gen checks a Generator's committed outputs for drift and writes fresh ones.
package gen

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/pmezard/go-difflib/difflib"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Drift is how a generator's committed outputs differ from a fresh render.
type Drift struct {
	// Diff is a unified diff from the committed outputs to the fresh ones.
	Diff string

	// Paths are the outputs that differ or are missing, sorted.
	Paths []string

	// InSync is true when every output matches.
	InSync bool
}

// Check renders g over cb and compares each output with the file cb holds; a missing
// file is drift from nothing.
func Check(ctx context.Context, cb interfaces.Codebase, g interfaces.Generator) (Drift, error) {
	out, err := g.Render(ctx, cb)
	if err != nil {
		return Drift{}, err
	}
	var diff strings.Builder
	d := Drift{}
	for _, p := range slices.Sorted(maps.Keys(out)) {
		have, from := "", p
		text, err := cb.Text(ctx, p)
		switch {
		case err == nil:
			have = string(text.Bytes)
		case apperr.Is(err, apperr.CodeNotFound):
			from = "/dev/null"
		default:
			return Drift{}, err
		}
		want := string(out[p])
		if have == want {
			continue
		}
		d.Paths = append(d.Paths, p)
		u, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
			A: lines(have), B: lines(want),
			FromFile: from, ToFile: p, Context: 3,
		})
		if err != nil {
			return Drift{}, apperr.Wrap(err, apperr.CodeInternal, "diff "+p)
		}
		diff.WriteString(u)
	}
	d.InSync, d.Diff = len(d.Paths) == 0, diff.String()
	return d, nil
}

// lines splits s into lines that keep their newline; a final newline adds no line.
func lines(s string) []string {
	out := strings.SplitAfter(s, "\n")
	if out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// Apply writes every artifact through write, in path order.
func Apply(
	ctx context.Context,
	write func(path string, data []byte) error,
	a types.Artifacts,
) error {
	for _, p := range slices.Sorted(maps.Keys(a)) {
		if err := ctx.Err(); err != nil {
			return apperr.Wrap(err, apperr.ContextCode(err, apperr.CodeCanceled), "apply")
		}
		if err := write(p, a[p]); err != nil {
			return err
		}
	}
	return nil
}
