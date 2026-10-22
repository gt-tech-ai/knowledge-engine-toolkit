// Package stub is the HelmClient that renders nothing and fetches nothing.
package stub

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Client is the stub HelmClient.
type Client struct{}

// Compile-time assertion that Client satisfies HelmClient.
var _ interfaces.HelmClient = Client{}

// Template renders an empty manifest.
func (Client) Template(
	context.Context,
	types.RenderSpec,
) ([]byte, error) {
	return nil, nil
}

// DependencyBuild does nothing.
func (Client) DependencyBuild(context.Context, string) error { return nil }

// Lint passes.
func (Client) Lint(context.Context, types.RenderSpec) error { return nil }
