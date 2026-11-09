package codebase

import (
	"path"
	"slices"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Matches reports whether sel selects path on its path alone: Roots, Paths, Globs,
// Exts and Exclude (TrackedOnly, IncludeGenerated and IncludeIgnored need the file,
// not its path).
// sel must not be nil.
func Matches(sel *types.FileSelector, p string) bool {
	return includedBy(sel, p) &&
		!slices.ContainsFunc(sel.Exclude, func(x string) bool { return excludes(x, p) })
}

// includedBy applies the selector's positive filters; an unset filter passes.
func includedBy(sel *types.FileSelector, p string) bool {
	return emptyOrAny(sel.Paths, func(c string) bool { return c == p }) &&
		emptyOrAny(sel.Roots, func(r string) bool { return under(p, r) }) &&
		emptyOrAny(sel.Globs, func(g string) bool { return Glob(g, p) }) &&
		emptyOrAny(sel.Exts, func(e string) bool { return strings.HasSuffix(p, e) })
}

// emptyOrAny reports whether a filter is unset or some entry passes.
func emptyOrAny(entries []string, pass func(string) bool) bool {
	return len(entries) == 0 || slices.ContainsFunc(entries, pass)
}

// excludes applies one exclude entry: a glob, or a path prefix.
func excludes(x, p string) bool {
	if strings.ContainsAny(x, "*?[") {
		return Glob(x, p)
	}

	return under(p, x)
}

// GeneratedName reports whether a Go file's name marks it generated: protobuf and
// gRPC output, `_gen.go` / `.gen.go` files, `zz_generated` deep-copy files and
// `mock_*.go` mocks.
func GeneratedName(p string) bool {
	base := path.Base(p)

	return strings.HasSuffix(base, ".pb.go") ||
		strings.HasSuffix(base, ".pb.gw.go") ||
		strings.HasSuffix(base, "_gen.go") ||
		strings.HasSuffix(base, ".gen.go") ||
		strings.HasPrefix(base, "zz_generated") ||
		(strings.HasPrefix(base, "mock_") && strings.HasSuffix(base, ".go"))
}

// GeneratedHeader reports whether Go source carries the standard generated-code
// marker, a `// Code generated … DO NOT EDIT.` line before the package clause.
func GeneratedHeader(src []byte) bool {
	for line := range strings.SplitSeq(string(src), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "package ") {
			return false
		}

		if strings.HasPrefix(line, "// Code generated ") &&
			strings.HasSuffix(line, " DO NOT EDIT.") {
			return true
		}
	}

	return false
}

// Glob reports whether the slash-separated path p matches pattern, where `**` matches
// any number of whole segments (including none) and every other segment matches as
// path.Match does. A malformed segment matches nothing.
func Glob(pattern, p string) bool {
	return globSegments(strings.Split(pattern, textSlash), strings.Split(p, textSlash))
}

// globSegments matches pattern segments against path segments, backtracking over `**`.
func globSegments(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			return globAnyDepth(pat[1:], segs)
		}

		if len(segs) == 0 || !segmentMatches(pat[0], segs[0]) {
			return false
		}

		pat, segs = pat[1:], segs[1:]
	}

	return len(segs) == 0
}

// globAnyDepth tries the rest of a pattern after a `**` at every remaining depth.
func globAnyDepth(rest, segs []string) bool {
	for i := 0; i <= len(segs); i++ {
		if globSegments(rest, segs[i:]) {
			return true
		}
	}

	return false
}

// segmentMatches matches one path segment; a malformed pattern matches nothing.
func segmentMatches(pat, seg string) bool {
	ok, err := path.Match(pat, seg)

	return err == nil && ok
}

// under reports whether p is dir itself or lies beneath it; "" and "." are the root.
func under(text, dir string) bool {
	dir = strings.TrimSuffix(dir, textSlash)
	if dir == "" || dir == "." {
		return true
	}

	return text == dir || strings.HasPrefix(text, dir+textSlash)
}

// ignoredScope returns where an IncludeIgnored selector looks for ignored files: its
// Paths, else its Roots, else each glob's literal leading directories. It returns nil
// (the whole tree) when the selector names none of them or a glob has no literal
// prefix, since the selector's own filters still apply to whatever is listed.
func ignoredScope(sel *types.FileSelector) []string {
	switch {
	case len(sel.Paths) > 0:
		return slices.Clone(sel.Paths)
	case len(sel.Roots) > 0:
		return slices.Clone(sel.Roots)
	}

	scope := make([]string, 0, len(sel.Globs))

	for _, g := range sel.Globs {
		lit := literalSegments(g)
		if len(lit) == 0 {
			return nil
		}

		scope = append(scope, path.Join(lit...))
	}

	slices.Sort(scope)

	return slices.Compact(scope)
}

// literalSegments returns a glob's leading segments that hold no wildcard.
func literalSegments(glob string) []string {
	var lit []string

	for seg := range strings.SplitSeq(glob, textSlash) {
		if strings.ContainsAny(seg, "*?[") {
			break
		}

		lit = append(lit, seg)
	}

	return lit
}
