package helm

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Decorate runs every HelmClient operation of base through the client tier as the
// client named name. All three are repeatable: rendering and linting read, and a
// dependency build converges on the lock file.
func Decorate(
	base interfaces.HelmClient,
	decs decorate.Decorations,
	name string,
	opts ...client.Option,
) (interfaces.HelmClient, error) {
	b, err := client.NewBase[interfaces.HelmClient](decs, name, opts...)
	if err != nil {
		return nil, err
	}
	return &decorator{base: base, Base: b}, nil
}

// decorator runs each HelmClient operation through the client tier.
type decorator struct {
	// base is the wrapped client.
	base interfaces.HelmClient

	// Base runs each operation through the client tier.
	client.Base
}

// Compile-time assertion that decorator satisfies HelmClient.
var _ interfaces.HelmClient = (*decorator)(nil)

// Template renders a chart.
func (d *decorator) Template(ctx context.Context, spec types.RenderSpec) ([]byte, error) {
	return d.Exec(ctx, "template", func(c context.Context) ([]byte, error) {
		return d.base.Template(c, spec)
	})
}

// DependencyBuild fetches a chart's dependencies.
func (d *decorator) DependencyBuild(ctx context.Context, chartDir string) error {
	return d.Do(ctx, "dependency_build", func(c context.Context) error {
		return d.base.DependencyBuild(c, chartDir)
	})
}

// Lint lints a chart.
func (d *decorator) Lint(ctx context.Context, spec types.RenderSpec) error {
	return d.Do(ctx, "lint", func(c context.Context) error {
		return d.base.Lint(c, spec)
	})
}
