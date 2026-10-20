package docker

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Decorate runs every DockerClient operation of base through the client tier as the
// client named name. Every operation converges when repeated (a build is cached, a
// push and a Compose up or down are idempotent), so each may retry.
func Decorate(
	base interfaces.DockerClient,
	decs decorate.Decorations,
	name string,
	opts ...client.Option,
) (interfaces.DockerClient, error) {
	b, err := client.NewBase[interfaces.DockerClient](decs, name, opts...)
	if err != nil {
		return nil, err
	}
	return &decorator{base: base, Base: b}, nil
}

// decorator runs each DockerClient operation through the client tier.
type decorator struct {
	// base is the wrapped client.
	base interfaces.DockerClient

	// Base runs each operation through the client tier.
	client.Base
}

// Compile-time assertion that decorator satisfies DockerClient.
var _ interfaces.DockerClient = (*decorator)(nil)

// Build builds an image.
func (d *decorator) Build(ctx context.Context, spec types.BuildSpec) error {
	return d.Do(
		ctx,
		"build",
		func(c context.Context) error { return d.base.Build(c, spec) },
	)
}

// Push pushes an image.
func (d *decorator) Push(ctx context.Context, ref string) error {
	return d.Do(ctx, "push", func(c context.Context) error { return d.base.Push(c, ref) })
}

// Inspect reads an image.
func (d *decorator) Inspect(ctx context.Context, ref string) ([]byte, error) {
	return d.Exec(ctx, "inspect", func(c context.Context) ([]byte, error) {
		return d.base.Inspect(c, ref)
	})
}

// ComposeUp starts a stack.
func (d *decorator) ComposeUp(ctx context.Context, spec types.ComposeSpec) error {
	return d.Do(ctx, "compose_up", func(c context.Context) error {
		return d.base.ComposeUp(c, spec)
	})
}

// ComposeDown stops a stack.
func (d *decorator) ComposeDown(ctx context.Context, spec types.ComposeSpec) error {
	return d.Do(ctx, "compose_down", func(c context.Context) error {
		return d.base.ComposeDown(c, spec)
	})
}
