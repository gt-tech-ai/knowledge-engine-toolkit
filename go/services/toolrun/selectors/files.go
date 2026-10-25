package selectors

import (
	"context"
	"regexp"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// heredocRe matches a COPY, RUN or ADD instruction carrying a heredoc (`<<`), which
// hadolint cannot parse.
var heredocRe = regexp.MustCompile(`(?m)^\s*(COPY|RUN|ADD)\b[^\n]*<<`)

// files is KindFiles.
type files struct{}

// Select returns the files matching spec.Glob or spec.Globs.
func (files) Select(
	ctx context.Context,
	cb interfaces.Codebase,
	spec types.TargetSpec,
) ([]string, error) {
	sel := types.FileSelector{IncludeGenerated: true, Globs: globs(spec)}
	found, err := cb.Files(ctx, sel)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(found))
	for _, f := range found {
		if spec.SkipHeredoc {
			text, err := cb.Text(ctx, f.Path)
			if err != nil {
				return nil, err
			}
			if heredocRe.Match(text.Bytes) {
				continue
			}
		}
		out = append(out, f.Path)
	}
	return out, nil
}

// fileDirs is KindFileDirs.
type fileDirs struct{}

// Select returns the distinct directories of the files files{} selects for spec.
func (fileDirs) Select(
	ctx context.Context,
	cb interfaces.Codebase,
	spec types.TargetSpec,
) ([]string, error) {
	found, err := files{}.Select(ctx, cb, spec)
	if err != nil {
		return nil, err
	}
	return dirsOf(found), nil
}
