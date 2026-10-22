package interfaces

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// DockerClient runs typed docker operations: image builds and pushes, and Compose
// stacks.
type DockerClient interface {
	// Build builds an image as spec describes.
	Build(ctx context.Context, spec types.BuildSpec) error

	// Push pushes the image ref.
	Push(ctx context.Context, ref string) error

	// Inspect returns the JSON of the local image ref; a missing image is
	// CodeNotFound.
	Inspect(ctx context.Context, ref string) ([]byte, error)

	// ComposeUp starts spec's stack and waits for it to be healthy.
	ComposeUp(ctx context.Context, spec types.ComposeSpec) error

	// ComposeDown stops spec's stack.
	ComposeDown(ctx context.Context, spec types.ComposeSpec) error
}
