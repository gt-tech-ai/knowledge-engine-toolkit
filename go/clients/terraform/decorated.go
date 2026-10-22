package terraform

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Decorate runs every TerraformClient operation of base through the client tier as the
// client named name. Apply changes infrastructure and may have partly landed, so it
// runs once; init, plan and show are repeatable.
func Decorate(
	base interfaces.TerraformClient,
	decs decorate.Decorations,
	name string,
	opts ...client.Option,
) (interfaces.TerraformClient, error) {
	b, err := client.NewBase[interfaces.TerraformClient](decs, name, opts...)
	if err != nil {
		return nil, err
	}
	return &decorator{base: base, Base: b}, nil
}

// decorator runs each TerraformClient operation through the client tier.
type decorator struct {
	// base is the wrapped client.
	base interfaces.TerraformClient

	// Base runs each operation through the client tier.
	client.Base
}

// Compile-time assertion that decorator satisfies TerraformClient.
var _ interfaces.TerraformClient = (*decorator)(nil)

// Init initializes a module.
func (d *decorator) Init(ctx context.Context, dir string) error {
	return d.Do(ctx, "init", func(c context.Context) error { return d.base.Init(c, dir) })
}

// Plan plans a module.
func (d *decorator) Plan(ctx context.Context, dir, out string) (types.Plan, error) {
	return d.Exec(ctx, "plan", func(c context.Context) (types.Plan, error) {
		return d.base.Plan(c, dir, out)
	})
}

// Apply applies a saved plan; it runs once.
func (d *decorator) Apply(ctx context.Context, dir, planFile string) error {
	return d.DoOnce(ctx, "apply", func(c context.Context) error {
		return d.base.Apply(c, dir, planFile)
	})
}

// Show reads a saved plan.
func (d *decorator) Show(ctx context.Context, dir, planFile string) (types.Plan, error) {
	return d.Exec(ctx, "show", func(c context.Context) (types.Plan, error) {
		return d.base.Show(c, dir, planFile)
	})
}
