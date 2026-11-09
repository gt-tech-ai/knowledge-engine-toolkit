package interfaces

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Renderer renders a chart to its manifest objects.
type Renderer interface {
	// Render renders spec and decodes its documents, in output order.
	// spec must not be nil.
	Render(ctx context.Context, spec *types.RenderSpec) ([]types.Object, error)
}
