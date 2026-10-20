package pins

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// generator renders the Renovate config to one path.
type generator struct {
	// path is where the config is written, relative to the codebase root.
	path string

	// policy is the consumer's update policy.
	policy Policy
}

// NewGenerator returns a Generator that renders the config for the codebase's
// ecosystems under policy to path (ConfigFileName when empty).
func NewGenerator(path string, policy Policy) interfaces.Generator {
	if path == "" {
		path = ConfigFileName
	}
	return &generator{path: path, policy: policy}
}

// Name identifies the generator.
func (g *generator) Name() string { return "renovate" }

// Inputs is every file: any file can add or remove an ecosystem.
func (g *generator) Inputs() types.FileSelector {
	return types.FileSelector{IncludeGenerated: true}
}

// Render detects the ecosystems in cb and renders the config.
func (g *generator) Render(ctx context.Context, cb interfaces.Codebase) (types.Artifacts, error) {
	e, err := Detect(ctx, cb)
	if err != nil {
		return nil, err
	}
	out, err := Render(e, g.policy)
	if err != nil {
		return nil, err
	}
	return types.Artifacts{g.path: out}, nil
}
