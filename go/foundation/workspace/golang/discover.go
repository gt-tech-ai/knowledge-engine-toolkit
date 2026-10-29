package golang

import (
	"io/fs"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// modFile is a module's go.mod file name.
const modFile = "go.mod"

// ModulePath reads the module path from the root go.mod of the repository fsys. A
// missing go.mod, or one with no module directive, is CodeNotFound.
func ModulePath(fsys fs.FS) (string, error) {
	data, err := fs.ReadFile(fsys, modFile)
	if err != nil {
		return "", apperr.Wrap(err, apperr.CodeNotFound, "open root go.mod")
	}
	mod := modfile.ModulePath(data)
	if mod == "" {
		return "", apperr.New(
			apperr.CodeNotFound,
			"no module directive found in root go.mod",
		)
	}
	return mod, nil
}

// HasGeneratedPackages reports whether pbDir (relative to the repository fsys) contains
// generated Go packages, detected by the presence of at least one *.pb.go file.
// Generated code is often gitignored and can be wiped by `git clean`/checkout, leaving
// a stale go.mod behind; callers must verify the packages themselves rather than
// trusting the module file alone, or downstream `go` commands fail with "module ...
// does not contain package". The walk short-circuits on the first match, so it stays
// cheap on a populated tree.
func HasGeneratedPackages(fsys fs.FS, pbDir string) bool {
	found := false
	_ = fs.WalkDir(fsys, pbDir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			//nolint:nilerr // best-effort scan: keep walking past unreadable entries
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".pb.go") {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}

// StagedModules discovers the Go modules of the repository fsys that contain the given
// staged files, each relative to the root. For every staged file it walks the directory
// hierarchy upward to the nearest go.mod (falling back to the repo-root module "."),
// dedups, and keeps a module only when keep (nil keeps all) accepts its relative path.
// Returns nil when no module is found: the staged-files counterpart of GoWorkModules
// for a tidy hook.
func StagedModules(
	fsys fs.FS,
	stagedFiles []string,
	keep func(relPath string) bool,
) []string {
	seen := make(map[string]struct{})
	var modules []string
	for _, f := range stagedFiles {
		mod, ok := owningModule(fsys, path.Dir(filepath.ToSlash(f)))
		if !ok {
			continue
		}
		if _, dup := seen[mod]; dup {
			continue
		}
		seen[mod] = struct{}{}
		if keep == nil || keep(mod) {
			modules = append(modules, mod)
		}
	}
	return modules
}

// owningModule walks up from dir to the nearest directory holding a go.mod, falling
// back to the root module "."; ok is false when not even the root has one.
func owningModule(fsys fs.FS, dir string) (string, bool) {
	for dir != "." && dir != "/" {
		if isFile(fsys, path.Join(dir, modFile)) { // nosemgrep: use-filepath-join
			return dir, true
		}
		dir = path.Dir(dir)
	}
	return ".", isFile(fsys, modFile)
}

// isFile reports whether name exists in fsys.
func isFile(fsys fs.FS, name string) bool {
	_, err := fs.Stat(fsys, name)
	return err == nil
}
