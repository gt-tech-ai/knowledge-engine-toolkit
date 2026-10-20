package interfaces

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// TerraformClient runs typed Terraform operations in a root module directory.
type TerraformClient interface {
	// Init initializes dir without prompting.
	Init(ctx context.Context, dir string) error

	// Plan plans dir into the plan file out and returns the parsed plan.
	Plan(ctx context.Context, dir, out string) (types.Plan, error)

	// Apply applies the saved plan file in dir.
	Apply(ctx context.Context, dir, planFile string) error

	// Show parses the saved plan file in dir.
	Show(ctx context.Context, dir, planFile string) (types.Plan, error)
}
