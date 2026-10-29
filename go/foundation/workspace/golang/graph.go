package golang

import (
	"io/fs"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// Module is one go.work module of a workspace graph.
type Module struct {
	// Dir is the module directory relative to the repository root ("." for the root).
	Dir string `json:"dir"`

	// Path is the module path its go.mod declares.
	Path string `json:"path"`

	// Requires are the directories of the workspace modules this one requires directly
	// (an `// indirect` require is skipped), in go.mod order.
	Requires []string `json:"requires,omitempty"`
}

// WorkspaceGraph reads the go.work of the repository fsys and every go.mod it uses, then
// returns ParseWorkspaceGraph's graph. A repository with no go.work is CodeNotFound; one
// that does not parse is CodeInvalidInput.
func WorkspaceGraph(fsys fs.FS) ([]Module, error) {
	data, err := fs.ReadFile(fsys, workFile)
	switch {
	case apperr.StdIs(err, fs.ErrNotExist):
		return nil, apperr.Wrap(err, apperr.CodeNotFound, "open go.work")
	case err != nil:
		return nil, apperr.Wrap(err, apperr.CodeInternal, "read go.work")
	}
	dirs, err := WorkUses(data)
	if err != nil {
		return nil, err
	}
	mods := make(map[string][]byte, len(dirs))
	for _, dir := range dirs {
		// fs.FS paths are slash-separated, so path.Join is the right join here.
		gomod := path.Join(dir, modFile) // nosemgrep: use-filepath-join
		if b, err := fs.ReadFile(fsys, gomod); err == nil {
			mods[dir] = b
		}
	}
	return ParseWorkspaceGraph(data, mods)
}

// WorkUses returns the module directories a go.work's use directives list, relative to
// the repository root ("." for the root), in file order. A go.work that does not parse
// is CodeInvalidInput.
func WorkUses(work []byte) ([]string, error) {
	wf, err := modfile.ParseWork(workFile, work, nil)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "parse go.work")
	}
	dirs := make([]string, 0, len(wf.Use))
	for _, use := range wf.Use {
		dirs = append(
			dirs,
			path.Clean(strings.TrimPrefix(filepath.ToSlash(use.Path), "./")),
		)
	}
	return dirs, nil
}

// ParseWorkspaceGraph returns every module the go.work work uses, the root module
// included, in go.work order, each with the workspace modules it requires directly.
// goMods holds each module's go.mod by its directory (as WorkUses spells it); a module
// whose go.mod is absent or does not parse is skipped, so a half-scaffolded module does
// not hide the rest of the graph. It does no I/O. A go.work that does not parse is
// CodeInvalidInput.
func ParseWorkspaceGraph(work []byte, goMods map[string][]byte) ([]Module, error) {
	dirs, err := WorkUses(work)
	if err != nil {
		return nil, err
	}
	var parsed []*modfile.File
	var out []Module
	dirOf := map[string]string{}
	for _, dir := range dirs {
		data, ok := goMods[dir]
		if !ok {
			continue
		}
		mf, err := modfile.ParseLax(dir+"/"+modFile, data, nil)
		if err != nil || mf.Module == nil {
			continue
		}
		dirOf[mf.Module.Mod.Path] = dir
		parsed = append(parsed, mf)
		out = append(out, Module{Dir: dir, Path: mf.Module.Mod.Path})
	}
	for i, mf := range parsed {
		for _, r := range mf.Require {
			if dep, ok := dirOf[r.Mod.Path]; ok && !r.Indirect {
				out[i].Requires = append(out[i].Requires, dep)
			}
		}
	}
	return out, nil
}
