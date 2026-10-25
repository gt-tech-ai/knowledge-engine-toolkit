// Package selectors holds the TargetSelector kinds a tool run picks its targets with:
// files, go_modules, python_projects, ts_workspace and staged_files.
package selectors

import (
	"context"
	"path"
	"slices"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/codebase"
)

const (
	// KindFiles selects the codebase files matching TargetSpec.Glob (every file when
	// empty), optionally dropping Dockerfiles that use a heredoc.
	KindFiles = "files"

	// KindFileDirs selects the distinct directories of the files KindFiles would select:
	// one target per directory, however many of its files match (a chart's tests dir).
	KindFileDirs = "file_dirs"

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

// NewFromConfig returns the selector kind names, narrowed by the spec's Roots and
// Exclude. An unknown kind, or KindStagedFiles without Deps.Git, is CodeInvalidInput.
func NewFromConfig(kind string, deps Deps) (interfaces.TargetSelector, error) {
	sel, err := newKind(kind, deps)
	if err != nil {
		return nil, err
	}
	return narrowed{inner: sel}, nil
}

// newKind returns the selector kind names, before the shared Roots and Exclude filters.
func newKind(kind string, deps Deps) (interfaces.TargetSelector, error) {
	switch kind {
	case KindFiles:
		return files{}, nil
	case KindFileDirs:
		return fileDirs{}, nil
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

// narrowed applies a spec's Roots and Exclude to its inner selector's targets.
type narrowed struct {
	// inner selects the targets.
	inner interfaces.TargetSelector
}

// Select returns inner's targets at or under one of spec.Roots (all of them when it is
// empty) and matching none of spec.Exclude, in inner's order.
func (n narrowed) Select(
	ctx context.Context,
	cb interfaces.Codebase,
	spec types.TargetSpec,
) ([]string, error) {
	targets, err := n.inner.Select(ctx, cb, spec)
	if err != nil || (len(spec.Roots) == 0 && len(spec.Exclude) == 0) {
		return targets, err
	}
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		if inRoots(t, spec.Roots) &&
			!slices.ContainsFunc(spec.Exclude, func(g string) bool {
				return codebase.Glob(g, t)
			}) {
			out = append(out, t)
		}
	}
	return out, nil
}

// inRoots reports whether p is one of roots or lies beneath one; no roots admits all.
func inRoots(p string, roots []string) bool {
	if len(roots) == 0 {
		return true
	}
	for _, r := range roots {
		r = path.Clean(r)
		if p == r || strings.HasPrefix(p, r+"/") {
			return true
		}
	}
	return false
}

// globs returns spec's Glob and Globs as one pattern list.
func globs(spec types.TargetSpec) []string {
	out := make([]string, 0, 1+len(spec.Globs))
	if spec.Glob != "" {
		out = append(out, spec.Glob)
	}
	return append(out, spec.Globs...)
}
