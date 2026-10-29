// Package python discovers a repository's Python packages: the directories holding a
// pyproject.toml under the given roots (Packages) and a package's importable module
// (ModuleName). It does no I/O of its own: the repository is read through an fs.FS
// rooted at it.
package python

import (
	"io/fs"
	"path"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// Packages discovers Python packages by walking each of roots (repository-relative
// directories) of the repository fsys for directories containing a
// pyproject.toml (a missing root is skipped; a Go-only tool directory has no
// pyproject.toml, so it contributes nothing). Paths are returned relative to the root
// (e.g. "libs/python/shelf"). An optional filter excludes packages by relative path
// (return false to skip).
func Packages(
	fsys fs.FS,
	roots []string,
	filter func(relPath string) bool,
) ([]string, error) {
	var packages []string
	for _, searchDir := range roots {
		entries, err := fs.ReadDir(fsys, searchDir)
		if apperr.StdIs(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, apperr.Wrap(err, apperr.CodeInternal, "read directory "+searchDir)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			err := findPyprojectDirs(fsys, path.Join(searchDir, entry.Name()), filter,
				&packages)
			if err != nil {
				return nil, err
			}
		}
	}
	return packages, nil
}

// findPyprojectDirs recursively finds directories of fsys containing pyproject.toml
// under dir and appends their paths to out.
func findPyprojectDirs(
	fsys fs.FS,
	dir string,
	filter func(string) bool,
	out *[]string,
) error {
	if _, err := fs.Stat(fsys, path.Join(dir, "pyproject.toml")); err == nil {
		if filter == nil || filter(dir) {
			*out = append(*out, dir)
		}
		return nil
	}
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "read directory "+dir)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := findPyprojectDirs(
			fsys,
			path.Join(dir, entry.Name()),
			filter,
			out,
		); err != nil {
			return err
		}
	}
	return nil
}

// ModuleName is the importable module of a src-layout package directory pkgDir of
// the repository fsys: the first real directory under <pkgDir>/src/
// (shelf/src/shelf → "shelf"), skipping build
// artifacts; the directory's base name without a src/ layout.
func ModuleName(fsys fs.FS, pkgDir string) string {
	entries, err := fs.ReadDir(fsys, path.Join(pkgDir, "src"))
	if err == nil {
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() && name != "__pycache__" &&
				!strings.HasSuffix(name, ".egg-info") {
				return name
			}
		}
	}
	return path.Base(pkgDir)
}
