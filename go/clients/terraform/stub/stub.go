// Package stub is the TerraformClient that plans no changes and applies nothing.
package stub

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Client is the stub TerraformClient.
type Client struct{}

// Compile-time assertion that Client satisfies TerraformClient.
var _ interfaces.TerraformClient = Client{}

// Init does nothing.
func (Client) Init(context.Context, string) error { return nil }

// Plan returns an empty plan.
func (Client) Plan(context.Context, string, string) (types.Plan, error) {
	return types.Plan{}, nil
}

// Apply does nothing.
func (Client) Apply(context.Context, string, string) error { return nil }

// Show returns an empty plan.
func (Client) Show(context.Context, string, string) (types.Plan, error) {
	return types.Plan{}, nil
}
