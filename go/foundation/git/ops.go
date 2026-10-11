package git

import (
	"context"
	"os/exec"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/system"
)

// HasPathsChanged reports whether any of the given paths differ between two
// git refs. It returns true when git diff --quiet exits non-zero (meaning
// there are differences).
func HasPathsChanged(
	ctx context.Context,
	r system.CommandRunner,
	root string,
	from string,
	to string,
	paths ...string,
) (bool, error) {
	args := make([]string, 0, 5+len(paths))
	args = append(args, "diff", "--quiet", from, to, "--")
	args = append(args, paths...)

	result := r.RunBuffered(ctx, root, "git", args...)
	if result.Err != nil {
		// git diff --quiet exits 1 when there are differences; that's the
		// normal "changed" signal, not a real error.
		var exitErr *exec.ExitError
		if apperr.As(result.Err, &exitErr) {
			return true, nil
		}
		return false, result.Err
	}
	return false, nil
}
