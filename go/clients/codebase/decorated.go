package codebase

import (
	"context"
	"regexp"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Decorate runs every Codebase operation of base through the client tier as the client
// named name: files, text, match, changed and rendered. Go and YAML return base's
// views, whose reads go through the same memo.
func Decorate(
	base interfaces.Codebase,
	decs decorate.Decorations,
	name string,
	opts ...client.Option,
) (interfaces.Codebase, error) {
	b, err := client.NewBase[interfaces.Codebase](decs, name, opts...)
	if err != nil {
		return nil, err
	}
	return &decorator{base: base, Base: b}, nil
}

// decorator runs each Codebase operation through the client tier.
type decorator struct {
	// base is the wrapped Codebase.
	base interfaces.Codebase

	// Base runs each operation through the client tier.
	client.Base
}

// Compile-time assertion that decorator satisfies Codebase.
var _ interfaces.Codebase = (*decorator)(nil)

// Files lists files; a read, so retryable.
func (d *decorator) Files(
	ctx context.Context,
	sel types.FileSelector,
) ([]types.File, error) {
	return d.Exec(ctx, "files", func(c context.Context) ([]types.File, error) {
		return d.base.Files(c, sel)
	})
}

// Text reads one file; a read, so retryable.
func (d *decorator) Text(ctx context.Context, path string) (types.Text, error) {
	return d.Exec(ctx, "text", func(c context.Context) (types.Text, error) {
		return d.base.Text(c, path)
	})
}

// Match scans files; a read, so retryable.
func (d *decorator) Match(
	ctx context.Context,
	sel types.FileSelector,
	patterns []*regexp.Regexp,
) ([]types.Hit, error) {
	return d.Exec(ctx, "match", func(c context.Context) ([]types.Hit, error) {
		return d.base.Match(c, sel, patterns)
	})
}

// Changed diffs against base; a read, so retryable.
func (d *decorator) Changed(ctx context.Context, base string) (map[string]bool, error) {
	return d.Exec(ctx, "changed", func(c context.Context) (map[string]bool, error) {
		return d.base.Changed(c, base)
	})
}

// Go returns the Go view.
func (d *decorator) Go() interfaces.GoView { return d.base.Go() }

// YAML returns the YAML view.
func (d *decorator) YAML() interfaces.YAMLView { return d.base.YAML() }

// Rendered renders a chart; deterministic, so retryable.
func (d *decorator) Rendered(
	ctx context.Context,
	spec types.RenderSpec,
) ([]types.Object, error) {
	return d.Exec(ctx, "rendered", func(c context.Context) ([]types.Object, error) {
		return d.base.Rendered(c, spec)
	})
}
