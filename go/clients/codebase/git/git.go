// Package git is the git working-tree CodebaseSource: the files git tracks plus the
// untracked files it does not ignore, read from disk, with Changed answered by
// `git diff --merge-base`. A directory that is not a repository has no files.
package git

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/codebase/fs"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// notARepo is git's exit code outside a repository.
const notARepo = 128

// Source serves a git working tree through git's own file list.
type Source struct {
	// runner runs git.
	runner interfaces.CommandRunner

	// root is the working-tree root.
	root string
}

// Compile-time assertion that Source satisfies CodebaseSource.
var _ interfaces.CodebaseSource = (*Source)(nil)

// New returns a Source for the working tree at root, running git through runner. It
// does no I/O.
func New(root string, runner interfaces.CommandRunner) *Source {
	return &Source{root: root, runner: runner}
}

// List returns the tracked files present on disk and the untracked, non-ignored
// files, sorted.
func (s *Source) List(ctx context.Context) ([]types.File, error) {
	tracked, err := s.lines(ctx, "ls-files", "-z", "--cached")
	if err != nil {
		return nil, err
	}
	untracked, err := s.lines(ctx, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	out := make([]types.File, 0, len(tracked)+len(untracked))
	for _, p := range tracked {
		// A tracked path deleted from the working tree is not a file to read.
		if _, err := os.Lstat(filepath.Join(s.root, filepath.FromSlash(p))); err == nil {
			out = append(out, types.File{Path: p})
		}
	}
	for _, p := range untracked {
		out = append(out, types.File{Path: p, Untracked: true})
	}
	slices.SortFunc(
		out,
		func(a, b types.File) int { return strings.Compare(a.Path, b.Path) },
	)
	return out, nil
}

// Read reads one file from the working tree.
func (s *Source) Read(_ context.Context, p string) ([]byte, error) {
	return fs.ReadFile(s.root, p)
}

// Changed returns the paths changed since the merge base with base plus the
// untracked files; an unresolvable base is CodeNotFound.
func (s *Source) Changed(ctx context.Context, base string) (map[string]bool, error) {
	if res := s.runner.RunBuffered(ctx, s.root, "git", "rev-parse", "--verify",
		"--quiet", base+"^{commit}"); res.Err != nil {
		return nil, apperr.Wrap(res.Err, apperr.CodeNotFound, "unknown base "+base)
	}
	diff, err := s.lines(ctx, "diff", "--name-only", "-z", "--merge-base", base)
	if err != nil {
		return nil, err
	}
	untracked, err := s.lines(ctx, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, p := range slices.Concat(diff, untracked) {
		out[p] = true
	}
	return out, nil
}

// lines runs git args and splits its NUL-delimited output; outside a repository it
// returns no lines.
func (s *Source) lines(ctx context.Context, args ...string) ([]string, error) {
	res := s.runner.RunBuffered(ctx, s.root, "git", args...)
	if res.ExitCode == notARepo {
		return nil, nil
	}
	if res.Err != nil {
		return nil, apperr.Wrap(res.Err, apperr.CodeUnavailable,
			"git "+strings.Join(args, " ")+": "+strings.TrimSpace(string(res.Stderr)))
	}
	out := client.SplitNUL(res.Stdout)
	for i, p := range out {
		out[i] = filepath.ToSlash(p)
	}
	return out, nil
}
