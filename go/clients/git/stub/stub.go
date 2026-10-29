// Package stub is the GitClient that touches nothing: every listing is empty, nothing
// has changed, and no directory is a repository.
package stub

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Client is the stub GitClient.
type Client struct{}

// Compile-time assertion that Client satisfies GitClient.
var _ interfaces.GitClient = Client{}

// HasPathsChanged reports no change.
func (Client) HasPathsChanged(
	context.Context,
	string,
	string,
	string,
	[]string,
) (bool, error) {
	return false, nil
}

// StageFiles does nothing.
func (Client) StageFiles(context.Context, string, []string) error { return nil }

// StagedFiles returns none.
func (Client) StagedFiles(context.Context, string, []string) ([]string, error) {
	return nil, nil
}

// LsFiles returns none.
func (Client) LsFiles(
	context.Context,
	string,
	[]string,
) ([]string, error) {
	return nil, nil
}

// ChangedSince returns none.
func (Client) ChangedSince(context.Context, string, string) ([]string, error) {
	return nil, nil
}

// Diff returns nothing.
func (Client) Diff(context.Context, string, ...string) ([]byte, error) { return nil, nil }

// RevParse returns ref unchanged.
func (Client) RevParse(
	_ context.Context,
	_, ref string,
) (string, error) {
	return ref, nil
}

// RemoteHasRef reports false: the stub has no remote.
func (Client) RemoteHasRef(context.Context, string, string, string) (bool, error) {
	return false, nil
}

// IsRepo reports false.
func (Client) IsRepo(context.Context, string) bool { return false }

// Tag returns no tag.
func (Client) Tag(context.Context, string) (string, error) { return "", nil }
