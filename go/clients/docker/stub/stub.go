// Package stub is the DockerClient with no daemon: builds, pushes and stacks succeed
// without effect, and no image exists.
package stub

import (
	"context"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Client is the stub DockerClient.
type Client struct{}

// Compile-time assertion that Client satisfies DockerClient.
var _ interfaces.DockerClient = Client{}

// Build does nothing.
func (Client) Build(context.Context, types.BuildSpec) error { return nil }

// Push does nothing.
func (Client) Push(context.Context, string) error { return nil }

// Inspect finds no image.
func (Client) Inspect(_ context.Context, ref string) ([]byte, error) {
	return nil, apperr.NotFound("no image " + ref)
}

// ComposeUp does nothing.
func (Client) ComposeUp(context.Context, types.ComposeSpec) error { return nil }

// ComposeDown does nothing.
func (Client) ComposeDown(context.Context, types.ComposeSpec) error { return nil }
