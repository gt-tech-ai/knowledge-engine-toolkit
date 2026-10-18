package secrets

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Decorate runs Get and Put of base through the client tier as the client named name.
// A Get may be retried (when the tier retries); a Put never is. The secret value never
// reaches a log or span: only the operation is observed.
func Decorate(
	base interfaces.Source,
	decs decorate.Decorations,
	name string,
	opts ...client.Option,
) (interfaces.Source, error) {
	b, err := client.NewBase[interfaces.Source](decs, name, opts...)
	if err != nil {
		return nil, err
	}
	return &decorated{base: base, Base: b}, nil
}

// decorated runs a credential Source's operations through the client tier.
type decorated struct {
	// base is the wrapped source.
	base interfaces.Source

	// Base runs each operation through the client tier.
	client.Base
}

var _ interfaces.Source = (*decorated)(nil)

// Get reads the credential ref names.
func (d *decorated) Get(ctx context.Context, ref types.Ref) (types.Secret, error) {
	return d.Exec(ctx, "get", func(c context.Context) (types.Secret, error) {
		return d.base.Get(c, ref)
	})
}

// Put writes the credential ref names; it is never replayed.
func (d *decorated) Put(ctx context.Context, ref types.Ref, value types.Secret) error {
	return d.DoOnce(ctx, "put", func(c context.Context) error {
		return d.base.Put(c, ref, value)
	})
}
