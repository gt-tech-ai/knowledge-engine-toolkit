package chaos

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Decorate runs every FaultInjector operation of base through the client tier as the
// client named name. Inject creates a resource (a retry would see its own fault as
// another run's), so it runs once; Revert is idempotent and Active a read.
func Decorate(
	base interfaces.FaultInjector,
	decs decorate.Decorations,
	name string,
	opts ...client.Option,
) (interfaces.FaultInjector, error) {
	b, err := client.NewBase[interfaces.FaultInjector](decs, name, opts...)
	if err != nil {
		return nil, err
	}
	return &decorator{base: base, Base: b}, nil
}

// decorator runs each FaultInjector operation through the client tier.
type decorator struct {
	// base is the wrapped injector.
	base interfaces.FaultInjector

	// Base runs each operation through the client tier.
	client.Base
}

// Compile-time assertion that decorator satisfies FaultInjector.
var _ interfaces.FaultInjector = (*decorator)(nil)

// Inject creates the fault; it runs once.
func (d *decorator) Inject(ctx context.Context, f types.Fault) error {
	return d.DoOnce(
		ctx,
		"inject",
		func(c context.Context) error { return d.base.Inject(c, f) },
	)
}

// Revert is idempotent.
func (d *decorator) Revert(ctx context.Context, f types.Fault) error {
	return d.Do(
		ctx,
		"revert",
		func(c context.Context) error { return d.base.Revert(c, f) },
	)
}

// Active is a read.
func (d *decorator) Active(ctx context.Context, namespace string) ([]string, error) {
	return d.Exec(ctx, "active", func(c context.Context) ([]string, error) {
		return d.base.Active(c, namespace)
	})
}
