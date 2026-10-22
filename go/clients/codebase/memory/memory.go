// Package memory is the in-memory CodebaseSource: a map of root-relative paths to
// contents, with no version control. It is the codebase family's default kind and the
// file fixture a check's unit test runs on.
package memory

import (
	"context"
	"maps"
	"slices"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Source serves files from a map.
type Source struct {
	// files maps a repository-relative path to its contents.
	files map[string]string
}

// Compile-time assertion that Source satisfies CodebaseSource.
var _ interfaces.CodebaseSource = (*Source)(nil)

// New returns a Source over a copy of files.
func New(files map[string]string) *Source {
	return &Source{files: maps.Clone(files)}
}

// List returns every path, sorted.
func (s *Source) List(context.Context) ([]types.File, error) {
	out := make([]types.File, 0, len(s.files))
	for _, p := range slices.Sorted(maps.Keys(s.files)) {
		out = append(out, types.File{Path: p})
	}
	return out, nil
}

// Read returns a path's contents; a missing path is CodeNotFound.
func (s *Source) Read(_ context.Context, p string) ([]byte, error) {
	body, ok := s.files[p]
	if !ok {
		return nil, apperr.NotFound("no file " + p)
	}
	return []byte(body), nil
}

// Changed returns an empty set: the source has no version control.
func (s *Source) Changed(context.Context, string) (map[string]bool, error) {
	return map[string]bool{}, nil
}
