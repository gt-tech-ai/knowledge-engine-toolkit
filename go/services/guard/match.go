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
	codebase interfaces.Codebase,
	guardRow *types.GuardRow,
	only int,
) ([]types.Finding, error) {
	set, err := compileMatchSet(guardRow, only)
	if err != nil {
		return nil, err
	}

	currentFiles, missing, err := files(ctx, codebase, guardRow)
	if err != nil || missing != nil {
		return missing, err
	}

	var out []types.Finding

	for _, file := range currentFiles {
		text, err := codebase.Text(ctx, file.Path)
		if err != nil {
			return nil, err
		}

		lines := active(text.Lines(), skippedPrefixes(guardRow, file.Path))

		out = append(out, set.requireFindings(file.Path, lines, formOf(guardRow))...)
		out = append(out, set.forbidFindings(file.Path, lines, formOf(guardRow))...)
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
func compileMatchSet(guardRow *types.GuardRow, only int) (*matchSet, error) {
	require, err := patterns(guardRow.Require, formOf(guardRow))
	if err != nil {
		return nil, err
	}

	if only >= 0 {
		require = require[only : only+1]
	}

	forbid, err := patterns(guardRow.Forbid, formOf(guardRow))
	if err != nil {
		return nil, err
	}

	allow, err := patterns(guardRow.Allow, formOf(guardRow))
	if err != nil {
		return nil, err
	}

	return &matchSet{require: require, forbid: forbid, allow: allow}, nil
}

// requireFindings reports each required pattern no line of the file matches.
func (s *matchSet) requireFindings(
	file string,
	lines []line,
	form patternForm,
) []types.Finding {
	var out []types.Finding

	for _, pattern := range s.require {
		found := slices.ContainsFunc(
			lines,
			func(l line) bool { return pattern.MatchString(l.text) },
		)
		if !found {
			out = append(out, types.Finding{
				File:    file,
				Rule:    "require",
				Message: file + " does not contain " + form.show(pattern),
				Check:   "",
				Line:    0,
			})
		}
	}

	return out
}

// forbidFindings reports each line a forbidden pattern hits and no allow excuses.
func (s *matchSet) forbidFindings(
	file string,
	lines []line,
	form patternForm,
) []types.Finding {
	out := make([]types.Finding, 0, len(s.forbid))

	for _, re := range s.forbid {
		out = append(out, s.forbiddenLines(re, file, lines, form)...)
	}

	return out
}

// forbiddenLines reports the lines one forbidden pattern hits.
func (s *matchSet) forbiddenLines(
	pattern *regexp.Regexp, file string, lines []line, form patternForm,
) []types.Finding {
	var out []types.Finding

	for _, l := range lines {
		if pattern.MatchString(l.text) && !matchesAny(s.allow, l.text) {
			out = append(out, types.Finding{
				File: file, Line: l.n, Rule: "forbid",
				Message: "contains " + form.show(pattern),
				Check:   "",
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

// skippedPrefixes is the comment markers whose lines the row ignores in file, or none
// when the row reads comments too.
func skippedPrefixes(guardRow *types.GuardRow, file string) []string {
	if !guardRow.SkipComments {
		return nil
	}

	return commentPrefixes(file)
}

// active returns text's lines, without the full-line comments that start with a prefix.
func active(text, prefixes []string) []line {
	out := make([]line, 0, len(text))

	for index, fileText := range text {
		trimmed := strings.TrimSpace(fileText)

		if slices.ContainsFunc(prefixes, func(p string) bool {
			return strings.HasPrefix(trimmed, p)
		}) {
			continue
		}

		out = append(out, line{text: fileText, n: index + 1})
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

// patternForm is how a row's patterns are written: as regular expressions or literally.
type patternForm struct {
	// quote prepares one pattern for compiling.
	quote func(string) string
	// show names a compiled pattern in a finding.
	show func(*regexp.Regexp) string
}

// formOf returns the form guardRow writes its patterns in.
func formOf(guardRow *types.GuardRow) patternForm {
	if guardRow.Literal {
		return patternForm{quote: regexp.QuoteMeta, show: func(re *regexp.Regexp) string {
			return `"` + strings.ReplaceAll(re.String(), `\`, "") + `"`
		}}
	}

	return patternForm{
		quote: func(pattern string) string { return pattern },
		show:  func(re *regexp.Regexp) string { return "/" + re.String() + "/" },
	}
}

// patterns compiles items in form.
func patterns(items []string, form patternForm) ([]*regexp.Regexp, error) {
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = form.quote(item)
	}

	return compileAll(quoted)
}
