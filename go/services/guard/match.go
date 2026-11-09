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
	set, err := compileMatchSet(r, only)
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

		lines := active(f.Path, text.Lines(), skipFlag(r.SkipComments))
		out = append(out, set.requireFindings(f.Path, lines, r.Literal)...)
		out = append(out, set.forbidFindings(f.Path, lines, r.Literal)...)
	}

	return out, nil
}

// matchSet is a match row's compiled patterns.
type matchSet struct {
	// require must each appear in a file.
	require []*regexp.Regexp

	// forbid must not appear on a line.
	forbid []*regexp.Regexp

	// allow excuses a line a forbid pattern hit.
	allow []*regexp.Regexp
}

// compileMatchSet compiles the row's patterns; only >= 0 narrows require to that one.
func compileMatchSet(r *types.GuardRow, only int) (*matchSet, error) {
	require, err := patterns(r.Require, literalFlag(r.Literal))
	if err != nil {
		return nil, err
	}

	if only >= 0 {
		require = require[only : only+1]
	}

	forbid, err := patterns(r.Forbid, literalFlag(r.Literal))
	if err != nil {
		return nil, err
	}

	allow, err := patterns(r.Allow, literalFlag(r.Literal))
	if err != nil {
		return nil, err
	}

	return &matchSet{require: require, forbid: forbid, allow: allow}, nil
}

// requireFindings reports each required pattern no line of the file matches.
func (s *matchSet) requireFindings(
	file string,
	lines []line,
	literal bool,
) []types.Finding {
	var out []types.Finding

	for _, re := range s.require {
		found := slices.ContainsFunc(
			lines,
			func(l line) bool { return re.MatchString(l.text) },
		)
		if !found {
			out = append(out, types.Finding{
				File: file, Rule: "require",
				Message: file + " does not contain " + show(re, literalFlag(literal)),
			})
		}
	}

	return out
}

// forbidFindings reports each line a forbidden pattern hits and no allow excuses.
func (s *matchSet) forbidFindings(
	file string,
	lines []line,
	literal bool,
) []types.Finding {
	out := make([]types.Finding, 0, len(s.forbid))

	for _, re := range s.forbid {
		out = append(out, s.forbiddenLines(re, file, lines, literal)...)
	}

	return out
}

// forbiddenLines reports the lines one forbidden pattern hits.
func (s *matchSet) forbiddenLines(
	re *regexp.Regexp, file string, lines []line, literal bool,
) []types.Finding {
	var out []types.Finding

	for _, l := range lines {
		if re.MatchString(l.text) && !matchesAny(s.allow, l.text) {
			out = append(out, types.Finding{
				File: file, Line: l.n, Rule: "forbid",
				Message: "contains " + show(re, literalFlag(literal)),
			})
		}
	}

	return out
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

// skipFlag names the skip option in a signature.
type skipFlag bool

// active returns text's lines, without full-line comments when skip is set.
func active(file string, text []string, skip skipFlag) []line {
	prefixes := commentPrefixes(file)
	out := make([]line, 0, len(text))

	for i, t := range text {
		trimmed := strings.TrimSpace(t)

		if bool(skip) && slices.ContainsFunc(prefixes, func(p string) bool {
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
func patterns(ps []string, literal literalFlag) ([]*regexp.Regexp, error) {
	if !literal {
		return compileAll(ps)
	}

	quoted := make([]string, len(ps))
	for i, p := range ps {
		quoted[i] = regexp.QuoteMeta(p)
	}

	return compileAll(quoted)
}

// literalFlag names the literal option in a signature.
type literalFlag bool

// show names a pattern in a finding.
func show(re *regexp.Regexp, literal literalFlag) string {
	if literal {
		return `"` + strings.ReplaceAll(re.String(), `\`, "") + `"`
	}

	return "/" + re.String() + "/"
}
