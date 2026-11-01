package git

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Decorate runs every GitClient operation of base through the client tier as the client
// named name. StageFiles, Move and Remove write the index and tree, so they run once;
// the rest are reads.
func Decorate(
	base interfaces.GitClient,
	decs decorate.Decorations,
	name string,
	opts ...client.Option,
) (interfaces.GitClient, error) {
	b, err := client.NewBase[interfaces.GitClient](decs, name, opts...)
	if err != nil {
		return nil, err
	}
	return &decorator{base: base, Base: b}, nil
}

// decorator runs each GitClient operation through the client tier.
type decorator struct {
	// base is the wrapped client.
	base interfaces.GitClient

	// Base runs each operation through the client tier.
	client.Base
}

// Compile-time assertion that decorator satisfies GitClient.
var _ interfaces.GitClient = (*decorator)(nil)

// HasPathsChanged is a read.
func (d *decorator) HasPathsChanged(
	ctx context.Context,
	root, from, to string,
	paths []string,
) (bool, error) {
	return d.Exec(ctx, "has_paths_changed", func(c context.Context) (bool, error) {
		return d.base.HasPathsChanged(c, root, from, to, paths)
	})
}

// StageFiles writes the index; it runs once.
func (d *decorator) StageFiles(ctx context.Context, root string, paths []string) error {
	return d.DoOnce(ctx, "stage_files", func(c context.Context) error {
		return d.base.StageFiles(c, root, paths)
	})
}

// StagedFiles is a read.
func (d *decorator) StagedFiles(
	ctx context.Context,
	root string,
	patterns []string,
) ([]string, error) {
	return d.Exec(ctx, "staged_files", func(c context.Context) ([]string, error) {
		return d.base.StagedFiles(c, root, patterns)
	})
}

// LsFiles is a read.
func (d *decorator) LsFiles(
	ctx context.Context,
	root string,
	pathspecs []string,
) ([]string, error) {
	return d.Exec(ctx, "ls_files", func(c context.Context) ([]string, error) {
		return d.base.LsFiles(c, root, pathspecs)
	})
}

// ChangedSince is a read.
func (d *decorator) ChangedSince(
	ctx context.Context,
	root, base string,
) ([]string, error) {
	return d.Exec(ctx, "changed_since", func(c context.Context) ([]string, error) {
		return d.base.ChangedSince(c, root, base)
	})
}

// Diff is a read.
func (d *decorator) Diff(
	ctx context.Context,
	root string,
	args ...string,
) ([]byte, error) {
	return d.Exec(ctx, "diff", func(c context.Context) ([]byte, error) {
		return d.base.Diff(c, root, args...)
	})
}

// RevParse is a read.
func (d *decorator) RevParse(ctx context.Context, root, ref string) (string, error) {
	return d.Exec(ctx, "rev_parse", func(c context.Context) (string, error) {
		return d.base.RevParse(c, root, ref)
	})
}

// RemoteHasRef is a read.
func (d *decorator) RemoteHasRef(
	ctx context.Context,
	root, remote, ref string,
) (bool, error) {
	return d.Exec(ctx, "remote_has_ref", func(c context.Context) (bool, error) {
		return d.base.RemoteHasRef(c, root, remote, ref)
	})
}

// IsRepo is a read; a decoration failure reports false.
func (d *decorator) IsRepo(ctx context.Context, root string) bool {
	ok, err := d.Exec(ctx, "is_repo", func(c context.Context) (bool, error) {
		return d.base.IsRepo(c, root), nil
	})
	return err == nil && ok
}

// Tag is a read.
func (d *decorator) Tag(ctx context.Context, root string) (string, error) {
	return d.Exec(ctx, "tag", func(c context.Context) (string, error) {
		return d.base.Tag(c, root)
	})
}

// Log is a read.
func (d *decorator) Log(
	ctx context.Context,
	root string,
	args ...string,
) ([]byte, error) {
	return d.Exec(ctx, "log", func(c context.Context) ([]byte, error) {
		return d.base.Log(c, root, args...)
	})
}

// Status is a read.
func (d *decorator) Status(
	ctx context.Context,
	root string,
	pathspecs []string,
) ([]byte, error) {
	return d.Exec(ctx, "status", func(c context.Context) ([]byte, error) {
		return d.base.Status(c, root, pathspecs)
	})
}

// LsUntracked is a read.
func (d *decorator) LsUntracked(
	ctx context.Context,
	root string,
	pathspecs []string,
) ([]string, error) {
	return d.Exec(ctx, "ls_untracked", func(c context.Context) ([]string, error) {
		return d.base.LsUntracked(c, root, pathspecs)
	})
}

// Grep is a read.
func (d *decorator) Grep(ctx context.Context, root, needle string) ([]string, error) {
	return d.Exec(ctx, "grep", func(c context.Context) ([]string, error) {
		return d.base.Grep(c, root, needle)
	})
}

// Move writes the tree and index; it runs once.
func (d *decorator) Move(ctx context.Context, root, from, to string) error {
	return d.DoOnce(ctx, "move", func(c context.Context) error {
		return d.base.Move(c, root, from, to)
	})
}

// Remove writes the tree and index; it runs once.
func (d *decorator) Remove(ctx context.Context, root string, paths []string) error {
	return d.DoOnce(ctx, "remove", func(c context.Context) error {
		return d.base.Remove(c, root, paths)
	})
}
