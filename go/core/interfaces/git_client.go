package interfaces

import "context"

// GitClient runs typed git operations against a working tree: GitOps' index and diff
// helpers plus file listing, revision lookup and tags.
type GitClient interface {
	GitOps

	// LsFiles returns the tracked files matching pathspecs (every tracked file when
	// none), relative to root; outside a repository it returns none.
	LsFiles(ctx context.Context, root string, pathspecs []string) ([]string, error)

	// ChangedSince returns the files that differ between the merge base with base and
	// the working tree.
	ChangedSince(ctx context.Context, root, base string) ([]string, error)

	// Diff returns `git diff` output for args.
	Diff(ctx context.Context, root string, args ...string) ([]byte, error)

	// RevParse resolves ref to a full object name; an unknown ref is CodeNotFound.
	RevParse(ctx context.Context, root, ref string) (string, error)

	// IsRepo reports whether root is inside a git working tree.
	IsRepo(ctx context.Context, root string) bool

	// Tag returns the tag pointing exactly at HEAD, or "" when there is none.
	Tag(ctx context.Context, root string) (string, error)
}
