// Package impact maps changed paths to what they affect: the units owning them (Map, a
// deploy row, a module, a chart) and the named pattern classes they touch (Classify, a
// language or a domain).
package impact

import (
	"cmp"
	"path"
	"slices"
	"strings"
)

// Map says which paths belong to which units.
type Map[T cmp.Ordered] struct {
	// Owned maps each unit to the path prefixes it owns.
	Owned map[T][]string

	// Shared are path prefixes every unit depends on (a shared module, an include).
	Shared []string

	// DispatchOnly units are never selected by a change; they run only on request.
	DispatchOnly []T
}

// Affected returns the units changed touches, sorted: every unit (but the
// dispatch-only ones) when a path is under a Shared prefix, else each unit owning a
// prefix a path is under. A prefix covers itself and everything below it; "" and "."
// cover every path.
func (m Map[T]) Affected(changed []string) []T {
	var out []T
	for unit, prefixes := range m.Owned {
		if slices.Contains(m.DispatchOnly, unit) {
			continue
		}
		if anyUnder(changed, m.Shared) || anyUnder(changed, prefixes) {
			out = append(out, unit)
		}
	}
	slices.Sort(out)
	return out
}

// anyUnder reports whether a path in paths is under one of prefixes.
func anyUnder(paths, prefixes []string) bool {
	for _, p := range paths {
		for _, pre := range prefixes {
			if under(path.Clean(p), pre) {
				return true
			}
		}
	}
	return false
}

// under reports whether p is prefix or below it.
func under(p, prefix string) bool {
	prefix = strings.TrimSuffix(path.Clean(prefix), "/")
	return prefix == "." || p == prefix || strings.HasPrefix(p, prefix+"/")
}
