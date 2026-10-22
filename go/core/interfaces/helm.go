package interfaces

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// HelmClient runs typed Helm operations on charts in a codebase.
type HelmClient interface {
	// Template renders spec's chart and returns the multi-document manifest.
	Template(ctx context.Context, spec types.RenderSpec) ([]byte, error)

	// DependencyBuild fetches the chart's dependencies into its charts/ directory.
	DependencyBuild(ctx context.Context, chartDir string) error

	// Lint lints spec's chart with spec's values.
	Lint(ctx context.Context, spec types.RenderSpec) error
}
