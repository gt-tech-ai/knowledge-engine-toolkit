// Package selectors holds the TargetSelector kinds a tool run picks its targets with:
// files, go_modules, python_projects, ts_workspace and staged_files.
package selectors

import (
	"path"
	"slices"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

const (
	// KindFiles selects the codebase files matching TargetSpec.Glob (every file when
	// empty), optionally dropping Dockerfiles that use a heredoc.
	KindFiles = "files"

	// KindGoModules selects the Go module directories (go.work `use` entries, else every
	// go.mod's directory), optionally only those owning a Go file.
	KindGoModules = "go_modules"

	// KindPythonProjects selects every directory holding a pyproject.toml.
	KindPythonProjects = "python_projects"

	// KindTSWorkspace selects the pnpm workspace package directories named by
	// pnpm-workspace.yaml.
	KindTSWorkspace = "ts_workspace"

	// KindStagedFiles selects the files staged in git, filtered by TargetSpec.Glob.
	KindStagedFiles = "staged_files"
)

// Deps carries what the git-backed kinds need.
type Deps struct {
	// Git lists staged files for KindStagedFiles.
	Git interfaces.GitOps

	// Root is the working-tree root KindStagedFiles asks git about.
	Root string
}

// NewFromConfig returns the selector kind names. An unknown kind, or KindStagedFiles
// without Deps.Git, is CodeInvalidInput.
func NewFromConfig(kind string, deps Deps) (interfaces.TargetSelector, error) {
	switch kind {
	case KindFiles:
		return files{}, nil
	case KindGoModules:
		return goModules{}, nil
	case KindPythonProjects:
		return pythonProjects{}, nil
	case KindTSWorkspace:
		return tsWorkspace{}, nil
	case KindStagedFiles:
		if deps.Git == nil {
			return nil, apperr.New(apperr.CodeInvalidInput,
				"selector staged_files: no git operations")
		}
		return stagedFiles{git: deps.Git, root: deps.Root}, nil
	default:
		return nil, apperr.New(apperr.CodeInvalidInput, "unknown target selector "+kind)
	}
}

// dirsOf returns the sorted, distinct directories of paths, "." for the root.
func dirsOf(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, path.Dir(p))
	}
	slices.Sort(out)
	return slices.Compact(out)
}
