// Package fs is the plain-directory CodebaseSource: every file under a root, except the
// `.git` directory, with no version control.
package fs

import (
	"context"
	iofs "io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Source serves the files under a root directory.
type Source struct {
	// root is the absolute or working-directory-relative repository root.
	root string
}

// Compile-time assertion that Source satisfies CodebaseSource.
var _ interfaces.CodebaseSource = (*Source)(nil)

// New returns a Source rooted at root. It does no I/O.
func New(root string) *Source {
	return &Source{root: root}
}

// List walks root, skipping `.git`, and returns every regular file, sorted.
func (s *Source) List(ctx context.Context) ([]types.File, error) {
	var out []types.File
	err := filepath.WalkDir(s.root, func(p string, d iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return apperr.Wrap(err, apperr.ContextCode(err, apperr.CodeCanceled), "walk")
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(s.root, p)
		if err != nil {
			return apperr.Wrap(err, apperr.CodeInternal, "relative path of "+p)
		}
		out = append(out, types.File{Path: filepath.ToSlash(rel)})
		return nil
	})
	if err != nil {
		return nil, apperr.Wrap(err, apperr.ContextCode(err, apperr.CodeInternal),
			"walk "+s.root)
	}
	// A walk orders by entry name, so "a/x" would precede "a-b/x"; the contract is
	// sorted by path.
	slices.SortFunc(
		out,
		func(a, b types.File) int { return strings.Compare(a.Path, b.Path) },
	)
	return out, nil
}

// Read reads one file through an os.Root, so a path can never escape the root; a
// missing file is CodeNotFound.
func (s *Source) Read(_ context.Context, p string) ([]byte, error) {
	return ReadFile(s.root, p)
}

// Changed returns an empty set: the source has no version control.
func (s *Source) Changed(context.Context, string) (map[string]bool, error) {
	return map[string]bool{}, nil
}

// ReadFile reads the repository-relative path p under root through an os.Root; a
// missing file is CodeNotFound and a path escaping the root CodeInvalidInput.
func ReadFile(root, p string) ([]byte, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "open "+root)
	}
	defer func() { _ = r.Close() }()
	data, err := r.ReadFile(filepath.FromSlash(p))
	switch {
	case err == nil:
		return data, nil
	case os.IsNotExist(err):
		return nil, apperr.Wrap(err, apperr.CodeNotFound, "no file "+p)
	default:
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "read "+p)
	}
}
