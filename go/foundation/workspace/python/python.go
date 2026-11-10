// Package python discovers a repository's Python packages: the directories holding a
// pyproject.toml under the given roots (Packages) and a package's importable module
// (ModuleName). It does no I/O of its own: the repository is read through an fs.FS
// rooted at it.
package python

import (
	"io/fs"
	"path"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
)

// Packages discovers Python packages by walking each of roots (repository-relative
// directories) of the repository fsys for directories containing a
// pyproject.toml (a missing root is skipped; a Go-only tool directory has no
// pyproject.toml, so it contributes nothing). A package's subdirectories are not
// searched. Paths are returned relative to the root (e.g. "libs/python/shelf"), in
// lexical walk order. An optional filter excludes packages by relative path (return
// false to skip).
func Packages(
	fsys fs.FS,
	roots []string,
	filter func(relPath string) bool,
) ([]string, error) {
	var packages []string

	for _, root := range roots {
		err := fs.WalkDir(
			fsys,
			root,
			func(dir string, entry fs.DirEntry, err error) error {
				return visit(fsys, root, dir, entry, err, filter, &packages)
			},
		)
		if err != nil {
			return nil, apperr.Wrap(
				err,
				apperr.CodeInternal,
				"find python packages under "+root,
			)
		}
	}

	return packages, nil
}

// visit handles one entry of the walk under root: the root itself goes to visitRoot, an
// unreadable directory is CodeInternal, and a directory below root holding a
// pyproject.toml is recorded (when filter allows it) and not descended into.
func visit(
	fsys fs.FS,
	root, dir string,
	entry fs.DirEntry,
	err error,
	filter func(string) bool,
	out *[]string,
) error {
	if dir == root {
		return visitRoot(dir, entry, err)
	}

	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "read directory "+dir)
	}

	if !entry.IsDir() || !hasPyproject(fsys, dir) {
		return nil
	}

	if filter == nil || filter(dir) {
		*out = append(*out, dir)
	}

	return fs.SkipDir
}

// visitRoot handles the walk's root: a missing root ends the walk with nothing found,
// and an unreadable root or one that is not a directory is CodeInternal.
func visitRoot(dir string, entry fs.DirEntry, err error) error {
	switch {
	case apperr.StdIs(err, fs.ErrNotExist):
		return fs.SkipAll
	case err != nil:
		return apperr.Wrap(err, apperr.CodeInternal, "read directory "+dir)
	case !entry.IsDir():
		return apperr.New(apperr.CodeInternal, "read directory "+dir+": not a directory")
	default:
		return nil
	}
}

// hasPyproject reports whether the directory dir of fsys holds a pyproject.toml. The
// lookup goes through fs.Sub, so an fs.FS path is never assembled by hand.
func hasPyproject(fsys fs.FS, dir string) bool {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		return false
	}

	_, err = fs.Stat(sub, "pyproject.toml")

	return err == nil
}

// ModuleName is the importable module of a src-layout package directory pkgDir of the
// repository fsys: the first real directory under <pkgDir>/src/ (shelf/src/shelf →
// "shelf"), skipping build artifacts; the directory's base name without a src/ layout.
func ModuleName(fsys fs.FS, pkgDir string) string {
	if name, ok := srcModule(fsys, pkgDir); ok {
		return name
	}

	return path.Base(pkgDir)
}

// srcModule returns the first real directory under <pkgDir>/src/ of fsys, skipping
// __pycache__ and *.egg-info build artifacts; ok is false when there is none or src/
// cannot be read.
func srcModule(fsys fs.FS, pkgDir string) (string, bool) {
	pkg, err := fs.Sub(fsys, pkgDir)
	if err != nil {
		return "", false
	}

	entries, err := fs.ReadDir(pkg, "src")
	if err != nil {
		return "", false
	}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() && name != "__pycache__" && !strings.HasSuffix(name, ".egg-info") {
			return name, true
		}
	}

	return "", false
}
