// Package git provides reusable git operations for the CLI and tooling layers.
// These helpers are used by fmt --staged, tidy --staged, and gen --if-changed
// to interact with the git index and diff machinery.
package git

import (
	"context"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/system"
)

// StagedFiles returns root-relative paths of staged files matching the given
// path patterns. It uses git diff --cached --name-only --diff-filter=ACM to
// list only added, copied, or modified files (excluding deletions).
func StagedFiles(
	ctx context.Context,
	r system.CommandRunner,
	root string,
	patterns ...string,
) ([]string, error) {
	args := make([]string, 0, 5+len(patterns))
	args = append(args, "diff", "--cached", "--name-only", "--diff-filter=ACM", "--")
	args = append(args, patterns...)

	result := r.RunBuffered(ctx, root, "git", args...)
	if result.Err != nil {
		return nil, result.Err
	}

	raw := strings.TrimSpace(string(result.Stdout))
	if raw == "" {
		return nil, nil
	}
	return strings.Split(raw, "\n"), nil
}

// StageFiles runs git add on the given paths.
func StageFiles(
	ctx context.Context,
	r system.CommandRunner,
	root string,
	paths ...string,
) error {
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"add", "--"}, paths...)
	return r.Run(ctx, root, "git", args...)
}
