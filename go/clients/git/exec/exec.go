// Package exec is the GitClient that runs the git binary through a CommandRunner.
package exec

import (
	"context"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// diffCmd is git's diff subcommand.
const diffCmd = "diff"

// notARepo is git's exit code outside a repository (and for an unknown revision).
const notARepo = 128

// Client runs git.
type Client struct {
	// runner runs git.
	runner interfaces.CommandRunner
}

// Compile-time assertion that Client satisfies GitClient.
var _ interfaces.GitClient = (*Client)(nil)

// New returns the Client over runner. It does no I/O.
func New(runner interfaces.CommandRunner) *Client { return &Client{runner: runner} }

// git runs git args in root and returns stdout or a coded error.
func (c *Client) git(ctx context.Context, root string, args ...string) ([]byte, error) {
	res := c.runner.RunBuffered(ctx, root, "git", args...)
	return client.Output(res, "git "+args[0])
}

// HasPathsChanged runs `git diff --quiet from to -- paths`; exit 1 means changed.
func (c *Client) HasPathsChanged(
	ctx context.Context,
	root, from, to string,
	paths []string,
) (bool, error) {
	args := append([]string{diffCmd, "--quiet", from, to, "--"}, paths...)
	res := c.runner.RunBuffered(ctx, root, "git", args...)
	if res.Err == nil {
		return false, nil
	}
	if res.ExitCode == 1 {
		return true, nil
	}
	_, err := client.Output(res, "git "+diffCmd)
	return false, err
}

// StageFiles runs `git add -- paths`; no paths is a no-op.
func (c *Client) StageFiles(ctx context.Context, root string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	_, err := c.git(ctx, root, append([]string{"add", "--"}, paths...)...)
	return err
}

// StagedFiles lists the added, copied or modified staged files matching patterns.
func (c *Client) StagedFiles(
	ctx context.Context,
	root string,
	patterns []string,
) ([]string, error) {
	args := append([]string{
		"diff", "--cached", "--name-only", "-z", "--diff-filter=ACM",
		"--",
	}, patterns...)
	out, err := c.git(ctx, root, args...)
	return client.SplitNUL(out), err
}

// LsFiles lists tracked files; outside a repository it returns none.
func (c *Client) LsFiles(
	ctx context.Context,
	root string,
	pathspecs []string,
) ([]string, error) {
	res := c.runner.RunBuffered(ctx, root, "git",
		append([]string{"ls-files", "-z", "--"}, pathspecs...)...)
	if res.ExitCode == notARepo {
		return nil, nil
	}
	out, err := client.Output(res, "git ls-files")
	return client.SplitNUL(out), err
}

// ChangedSince lists the files changed since the merge base with base.
func (c *Client) ChangedSince(ctx context.Context, root, base string) ([]string, error) {
	out, err := c.git(ctx, root, diffCmd, "--name-only", "-z", "--merge-base", base)
	return client.SplitNUL(out), err
}

// Diff returns `git diff args…`.
func (c *Client) Diff(ctx context.Context, root string, args ...string) ([]byte, error) {
	return c.git(ctx, root, append([]string{diffCmd}, args...)...)
}

// RevParse resolves ref; an unknown ref is CodeNotFound.
func (c *Client) RevParse(ctx context.Context, root, ref string) (string, error) {
	res := c.runner.RunBuffered(ctx, root, "git", "rev-parse", "--verify", "--quiet", ref)
	if res.Err != nil {
		return "", apperr.Wrap(res.Err, apperr.CodeNotFound, "unknown revision "+ref)
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}

// noMatchingRefs is `git ls-remote --exit-code`'s exit status when no ref matches.
const noMatchingRefs = 2

// RemoteHasRef runs `git ls-remote --exit-code --heads --tags remote ref`: exit 0 is a
// match, exit 2 none, and any other failure a remote that could not be asked.
func (c *Client) RemoteHasRef(
	ctx context.Context,
	root, remote, ref string,
) (bool, error) {
	res := c.runner.RunBuffered(ctx, root, "git", "ls-remote", "--exit-code", "--heads",
		"--tags", remote, ref)
	switch {
	case res.Err == nil:
		return true, nil
	case res.ExitCode == noMatchingRefs:
		return false, nil
	default:
		return false, apperr.Wrap(res.Err, apperr.CodeUnavailable,
			"git ls-remote "+remote+": "+strings.TrimSpace(string(res.Stderr)))
	}
}

// IsRepo reports whether root is inside a working tree.
func (c *Client) IsRepo(ctx context.Context, root string) bool {
	res := c.runner.RunBuffered(ctx, root, "git", "rev-parse", "--is-inside-work-tree")
	return res.Err == nil && strings.TrimSpace(string(res.Stdout)) == "true"
}

// Tag returns the tag at HEAD, or "" when HEAD is untagged.
func (c *Client) Tag(ctx context.Context, root string) (string, error) {
	res := c.runner.RunBuffered(
		ctx,
		root,
		"git",
		"describe",
		"--tags",
		"--exact-match",
		"HEAD",
	)
	if res.ExitCode == notARepo {
		return "", nil
	}
	out, err := client.Output(res, "git describe")
	return strings.TrimSpace(string(out)), err
}
