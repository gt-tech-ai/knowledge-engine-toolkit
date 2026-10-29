package auth0

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Decorate runs every Auth0Management call of base through the client tier as the
// client named name. Every call is a read, so each retries.
func Decorate(
	base interfaces.Auth0Management,
	decs decorate.Decorations,
	name string,
	opts ...client.Option,
) (interfaces.Auth0Management, error) {
	b, err := client.NewBase[interfaces.Auth0Management](decs, name, opts...)
	if err != nil {
		return nil, err
	}
	return &decorator{base: base, Base: b}, nil
}

// decorator runs each Auth0Management call through the client tier.
type decorator struct {
	// base is the wrapped client.
	base interfaces.Auth0Management

	// Base runs each call through the client tier.
	client.Base
}

// Compile-time assertion that decorator satisfies Auth0Management.
var _ interfaces.Auth0Management = (*decorator)(nil)

// OrganizationIDs is a read.
func (d *decorator) OrganizationIDs(ctx context.Context) ([]string, error) {
	return d.Exec(ctx, decorate.OpName("OrganizationIDs"), d.base.OrganizationIDs)
}

// OrganizationID is a read.
func (d *decorator) OrganizationID(ctx context.Context, name string) (string, error) {
	return d.Exec(
		ctx,
		decorate.OpName("OrganizationID"),
		func(c context.Context) (string, error) {
			return d.base.OrganizationID(c, name)
		},
	)
}

// SigningKey is a read.
func (d *decorator) SigningKey(ctx context.Context) (types.SigningKey, error) {
	return d.Exec(ctx, decorate.OpName("SigningKey"), d.base.SigningKey)
}
