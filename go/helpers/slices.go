package helpers

import "path/filepath"

// Unique returns a new slice containing only the first occurrence of each element,
// preserving order.
func Unique[T comparable](s []T) []T {
	seen := make(map[T]struct{}, len(s))
	out := make([]T, 0, len(s))
	for _, v := range s {
		if _, ok := seen[v]; !ok {
			seen[v] = struct{}{}
			out = append(out, v)
		}
	}
	return out
}

// UniqueDirs extracts the directory component of each path in paths and returns
// the unique set, preserving order of first occurrence.
func UniqueDirs(paths []string) []string {
	dirs := make([]string, len(paths))
	for i, p := range paths {
		dirs[i] = filepath.Dir(p)
	}
	return Unique(dirs)
}
