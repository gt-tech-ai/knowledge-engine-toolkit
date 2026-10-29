// Package golang discovers the Go side of a workspace: the go.work modules
// (GoWorkModules), which of them hold loadable Go packages (FilterModulesWithGoPackages,
// GoListPackages, HasGoPackagesWithTags — `go list` run through the injected
// CommandRunner and memoized per directory), scoped go.work files for isolated runs
// (ScopedWork), the root module path (ModulePath), the modules holding staged files
// (StagedModules) and whether generated protobuf packages are present
// (HasGeneratedPackages). It does no I/O of its own: the workspace is read through an
// fs.FS rooted at the repository, and `go list` runs through the injected
// CommandRunner. go.work and go.mod are parsed by golang.org/x/mod/modfile.
package golang

import (
	"io/fs"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// workFile is the workspace file's name at the repository root.
const workFile = "go.work"

// GoWorkModules parses the go.work of the repository fsys and returns the module
// directories its use directives list, relative to the root (e.g. "libs/core",
// "services/catalog"), in file order. The root module (".") is excluded. An optional
// filter excludes modules by relative path (return false to skip). A repository with
// no go.work is CodeNotFound; one that does not parse is CodeInvalidInput.
func GoWorkModules(fsys fs.FS, filter func(relPath string) bool) ([]string, error) {
	work, err := parseWork(fsys)
	if err != nil {
		return nil, err
	}
	var modules []string
	for _, use := range work.Use {
		mod := strings.TrimPrefix(filepath.ToSlash(use.Path), "./")
		if mod == "" || mod == "." {
			continue
		}
		if filter != nil && !filter(mod) {
			continue
		}
		modules = append(modules, mod)
	}
	return modules, nil
}

// parseWork reads and parses the go.work of the repository fsys: a missing one is
// CodeNotFound, an unreadable one CodeInternal, a malformed one CodeInvalidInput.
func parseWork(fsys fs.FS) (*modfile.WorkFile, error) {
	data, err := fs.ReadFile(fsys, workFile)
	switch {
	case apperr.StdIs(err, fs.ErrNotExist):
		return nil, apperr.Wrap(err, apperr.CodeNotFound, "open go.work")
	case err != nil:
		return nil, apperr.Wrap(err, apperr.CodeInternal, "read go.work")
	}
	work, err := modfile.ParseWork(workFile, data, nil)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "parse go.work")
	}
	return work, nil
}

// ScopedWork renders a go.work listing only the given module directories (as absolute
// paths under root, the directory fsys is rooted at), plus the root go.work's
// toolchain, godebug and replace directives, so a scoped run resolves dependencies and
// the toolchain exactly as the full workspace does. A local-path replace target is made
// absolute, since the caller writes the file outside the repository, where an
// interrupted run cannot leave a stray go.work in the working tree. The caller writes
// it and points GOWORK at it. A go.work with no go directive is CodeInvalidInput.
func ScopedWork(fsys fs.FS, root string, moduleDirs ...string) ([]byte, error) {
	work, err := parseWork(fsys)
	if err != nil {
		return nil, err
	}
	if work.Go == nil || work.Go.Version == "" {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"no 'go' directive found in go.work",
		)
	}
	scoped := &modfile.WorkFile{Syntax: &modfile.FileSyntax{}}
	if err := scopeDirectives(scoped, work, root); err != nil {
		return nil, err
	}
	for _, dir := range moduleDirs {
		if err := scoped.AddUse(filepath.Join(root, dir), ""); err != nil {
			return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "use "+dir)
		}
	}
	scoped.Cleanup()
	return modfile.Format(scoped.Syntax), nil
}

// scopeDirectives copies work's go, toolchain, godebug and replace directives into
// scoped, each local replace target made absolute against root.
func scopeDirectives(scoped, work *modfile.WorkFile, root string) error {
	if err := scoped.AddGoStmt(work.Go.Version); err != nil {
		return apperr.Wrap(err, apperr.CodeInvalidInput, "go directive")
	}
	if work.Toolchain != nil {
		if err := scoped.AddToolchainStmt(work.Toolchain.Name); err != nil {
			return apperr.Wrap(err, apperr.CodeInvalidInput, "toolchain directive")
		}
	}
	for _, g := range work.Godebug {
		if err := scoped.AddGodebug(g.Key, g.Value); err != nil {
			return apperr.Wrap(err, apperr.CodeInvalidInput, "godebug "+g.Key)
		}
	}
	for _, r := range work.Replace {
		target := r.New.Path
		if modfile.IsDirectoryPath(target) && !filepath.IsAbs(target) {
			target = filepath.Join(root, target)
		}
		err := scoped.AddReplace(r.Old.Path, r.Old.Version, target, r.New.Version)
		if err != nil {
			return apperr.Wrap(err, apperr.CodeInvalidInput, "replace "+r.Old.Path)
		}
	}
	return nil
}
