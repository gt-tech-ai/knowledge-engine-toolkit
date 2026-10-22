package codebase

import (
	"path"
	"slices"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Matches reports whether sel selects path on its path alone: Roots, Paths, Globs,
// Exts and Exclude (TrackedOnly and IncludeGenerated need the file, not its path).
func Matches(sel types.FileSelector, p string) bool {
	if len(sel.Paths) > 0 && !slices.Contains(sel.Paths, p) {
		return false
	}
	if len(sel.Roots) > 0 && !slices.ContainsFunc(sel.Roots, func(r string) bool {
		return under(p, r)
	}) {
		return false
	}
	if len(sel.Globs) > 0 && !slices.ContainsFunc(sel.Globs, func(g string) bool {
		return Glob(g, p)
	}) {
		return false
	}
	if len(sel.Exts) > 0 && !slices.ContainsFunc(sel.Exts, func(e string) bool {
		return strings.HasSuffix(p, e)
	}) {
		return false
	}
	return !slices.ContainsFunc(sel.Exclude, func(x string) bool {
		if strings.ContainsAny(x, "*?[") {
			return Glob(x, p)
		}
		return under(p, x)
	})
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
	return globSegments(strings.Split(pattern, "/"), strings.Split(p, "/"))
}

// globSegments matches pattern segments against path segments, backtracking over `**`.
func globSegments(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			rest := pat[1:]
			for i := 0; i <= len(segs); i++ {
				if globSegments(rest, segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		if ok, err := path.Match(pat[0], segs[0]); err != nil || !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}

// under reports whether p is dir itself or lies beneath it; "" and "." are the root.
func under(p, dir string) bool {
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" || dir == "." {
		return true
	}
	return p == dir || strings.HasPrefix(p, dir+"/")
}
