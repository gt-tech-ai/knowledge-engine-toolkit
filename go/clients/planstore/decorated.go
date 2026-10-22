package planstore

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Decorate runs every PlanStore operation of base through the client tier as the
// client named name. Save writes the manifest and Load creates files exclusively, so a
// retry of either would trip over its own first attempt: both run once (the storage
// client retries each object call itself).
func Decorate(
	base interfaces.PlanStore,
	decs decorate.Decorations,
	name string,
	opts ...client.Option,
) (interfaces.PlanStore, error) {
	b, err := client.NewBase[interfaces.PlanStore](decs, name, opts...)
	if err != nil {
		return nil, err
	}
	return &decorator{base: base, Base: b}, nil
}

// decorator runs each PlanStore operation through the client tier.
type decorator struct {
	// base is the wrapped store.
	base interfaces.PlanStore

	// Base runs each operation through the client tier.
	client.Base
}

// Compile-time assertion that decorator satisfies PlanStore.
var _ interfaces.PlanStore = (*decorator)(nil)

// Save runs once.
func (d *decorator) Save(ctx context.Context, set types.PlanSet) error {
	return d.DoOnce(
		ctx,
		"save",
		func(c context.Context) error { return d.base.Save(c, set) },
	)
}

// Load runs once.
func (d *decorator) Load(ctx context.Context, set types.PlanSet) error {
	return d.DoOnce(
		ctx,
		"load",
		func(c context.Context) error { return d.base.Load(c, set) },
	)
}
