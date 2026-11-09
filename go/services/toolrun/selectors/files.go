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
// spec must not be nil.
func (files) Select(
	ctx context.Context,
	cb interfaces.Codebase,
	spec *types.TargetSpec,
) ([]string, error) {
	sel := types.FileSelector{IncludeGenerated: true, Globs: globs(spec)}

	found, err := cb.Files(ctx, &sel)
	if err != nil {
		return nil, err
	}

	out := make([]string, 0, len(found))

	for _, f := range found {
		keep, err := keepFile(ctx, cb, spec, f.Path)
		if err != nil {
			return nil, err
		}

		if keep {
			out = append(out, f.Path)
		}
	}

	return out, nil
}

// keepFile applies the spec's heredoc exclusion, reading the file only when it is on.
func keepFile(
	ctx context.Context, cb interfaces.Codebase, spec *types.TargetSpec, p string,
) (bool, error) {
	if !spec.SkipHeredoc {
		return true, nil
	}

	text, err := cb.Text(ctx, p)
	if err != nil {
		return false, err
	}

	return !heredocRe.Match(text.Bytes), nil
}

// fileDirs is KindFileDirs.
type fileDirs struct{}

// Select returns the distinct directories of the files files{} selects for spec.
// spec must not be nil.
func (fileDirs) Select(
	ctx context.Context,
	cb interfaces.Codebase,
	spec *types.TargetSpec,
) ([]string, error) {
	found, err := files{}.Select(ctx, cb, spec)
	if err != nil {
		return nil, err
	}

	return dirsOf(found), nil
}
