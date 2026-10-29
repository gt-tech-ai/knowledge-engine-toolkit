package guard

import (
	"context"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// matchCheck reports, for every selected file, each Require pattern (only the one at
// only, when only ≥ 0) no line matches and each line a Forbid pattern matches, unless an
// Allow pattern matches the line too.
func matchCheck(
	ctx context.Context,
	cb interfaces.Codebase,
	r *types.GuardRow,
	only int,
) ([]types.Finding, error) {
	require, err := patterns(r.Require, r.Literal)
	if err != nil {
		return nil, err
	}
	if only >= 0 {
		require = require[only : only+1]
	}
	forbid, err := patterns(r.Forbid, r.Literal)
	if err != nil {
		return nil, err
	}
	allow, err := patterns(r.Allow, r.Literal)
	if err != nil {
		return nil, err
	}
	fs, missing, err := files(ctx, cb, r)
	if err != nil || missing != nil {
		return missing, err
	}
	var out []types.Finding
	for _, f := range fs {
		text, err := cb.Text(ctx, f.Path)
		if err != nil {
			return nil, err
		}
		lines := active(f.Path, text.Lines(), r.SkipComments)
		for _, re := range require {
			if !slices.ContainsFunc(
				lines,
				func(l line) bool { return re.MatchString(l.text) },
			) {
				out = append(out, types.Finding{
					File: f.Path, Rule: "require",
					Message: f.Path + " does not contain " + show(re, r.Literal),
				})
			}
		}
		for _, re := range forbid {
			for _, l := range lines {
				if re.MatchString(l.text) && !matchesAny(allow, l.text) {
					out = append(out, types.Finding{
						File: f.Path, Line: l.n, Rule: "forbid",
						Message: "contains " + show(re, r.Literal),
					})
				}
			}
		}
	}
	return out, nil
}

// matchesAny reports whether one of res matches s.
func matchesAny(res []*regexp.Regexp, s string) bool {
	return slices.ContainsFunc(
		res,
		func(re *regexp.Regexp) bool { return re.MatchString(s) },
	)
}

// line is one active line and its 1-based number.
type line struct {
	// text is the line.
	text string

	// n is its number.
	n int
}

// active returns text's lines, without full-line comments when skip is set.
func active(file string, text []string, skip bool) []line {
	prefixes := commentPrefixes(file)
	out := make([]line, 0, len(text))
	for i, t := range text {
		trimmed := strings.TrimSpace(t)
		if skip && slices.ContainsFunc(prefixes, func(p string) bool {
			return strings.HasPrefix(trimmed, p)
		}) {
			continue
		}
		out = append(out, line{text: t, n: i + 1})
	}
	return out
}

// commentPrefixes returns the full-line comment markers of file's type; a type with none
// (Markdown, JSON, templates) has every line active.
func commentPrefixes(file string) []string {
	base := path.Base(file)
	ext := path.Ext(file)
	switch {
	case ext == ".yaml", ext == ".yml", ext == ".sh", base == "Dockerfile",
		strings.HasPrefix(base, "Dockerfile."), strings.HasSuffix(base, ".Dockerfile"):
		return []string{"#"}
	case ext == ".tf", ext == ".hcl":
		return []string{"#", "//"}
	case ext == ".alloy":
		return []string{"//"}
	}
	return nil
}

// patterns compiles ps, quoting each first when literal.
func patterns(ps []string, literal bool) ([]*regexp.Regexp, error) {
	if !literal {
		return compileAll(ps)
	}
	quoted := make([]string, len(ps))
	for i, p := range ps {
		quoted[i] = regexp.QuoteMeta(p)
	}
	return compileAll(quoted)
}

// show names a pattern in a finding.
func show(re *regexp.Regexp, literal bool) string {
	if literal {
		return `"` + strings.ReplaceAll(re.String(), `\`, "") + `"`
	}
	return "/" + re.String() + "/"
}
