package interfaces

import "context"

// GitClient runs typed git operations against a working tree: GitOps' index and diff
// helpers plus file listing, revision lookup and tags.
type GitClient interface {
	GitOps
	GitRevisions
	GitWorkingTree
}

// GitRevisions reads history and revisions: diffs, revision lookup, remotes, tags and the
// log.
type GitRevisions interface {
	// ChangedSince returns the files that differ between the merge base with base and
	// the working tree.
	ChangedSince(ctx context.Context, root, base string) ([]string, error)

	// Diff returns `git diff` output for args.
	Diff(ctx context.Context, root string, args ...string) ([]byte, error)

	// RevParse resolves ref to a full object name; an unknown ref is CodeNotFound.
	RevParse(ctx context.Context, root, ref string) (string, error)

	// RemoteHasRef reports whether remote carries a branch or tag named ref; a remote
	// that cannot be asked (no credentials, no network) is CodeUnavailable.
	RemoteHasRef(ctx context.Context, root, remote, ref string) (bool, error)

	// IsRepo reports whether root is inside a git working tree.
	IsRepo(ctx context.Context, root string) bool

	// Tag returns the tag pointing exactly at HEAD, or "" when there is none.
	Tag(ctx context.Context, root string) (string, error)

	// Log returns `git log` output for args (the caller picks the format and limits).
	Log(ctx context.Context, root string, args ...string) ([]byte, error)
}

// GitWorkingTree lists, searches and changes the files of a working tree.
type GitWorkingTree interface {
	// LsFiles returns the tracked files matching pathspecs (every tracked file when
	// none), relative to root; outside a repository it returns none.
	LsFiles(ctx context.Context, root string, pathspecs []string) ([]string, error)

	// Status returns `git status --porcelain` output for pathspecs (the whole tree when
	// none): one "XY path" line per changed or untracked file.
	Status(ctx context.Context, root string, pathspecs []string) ([]byte, error)

	// LsUntracked returns the untracked files matching pathspecs that .gitignore does not
	// exclude, relative to root.
	LsUntracked(ctx context.Context, root string, pathspecs []string) ([]string, error)

	// Grep returns the tracked text files containing needle as a fixed string; no match
	// is none, not an error.
	Grep(ctx context.Context, root, needle string) ([]string, error)

	// Move renames the path from to the path to, in the working tree and the index
	// (`git mv`).
	Move(ctx context.Context, root, from, to string) error

	// Remove deletes paths from the working tree and the index (`git rm -f`); no paths
	// is a no-op.
	Remove(ctx context.Context, root string, paths []string) error
}
