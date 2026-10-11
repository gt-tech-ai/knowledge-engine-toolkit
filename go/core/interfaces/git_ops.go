package interfaces

import "context"

// GitOps abstracts git index and diff operations needed by conditional and
// staged-file job wrappers. Implementations live in the CLI layer (e.g.,
// foundation/git.Ops) so consumer packages stay free of git dependencies.
type GitOps interface {
	// HasPathsChanged reports whether any of the given paths differ between
	// two git refs. Returns true when differences exist.
	HasPathsChanged(
		ctx context.Context,
		root, from, to string,
		paths []string,
	) (bool, error)

	// StageFiles runs git add on the given paths relative to root.
	StageFiles(ctx context.Context, root string, paths []string) error

	// StagedFiles returns root-relative paths of staged files matching the
	// given glob patterns (e.g., "*.go", "*.py").
	StagedFiles(ctx context.Context, root string, patterns []string) ([]string, error)
}
