// Package stub is the LLMClient that makes no call: it answers every request with a
// fixed text and zero usage, so a composition root boots with zero infrastructure.
package stub

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Text is the stub's reply to every request.
const Text = "stub completion"

// Client is the stub LLMClient.
type Client struct{}

// Compile-time assertion that Client satisfies LLMClient.
var _ interfaces.LLMClient = Client{}

// New returns the stub.
func New() Client { return Client{} }

// Complete returns Text for the requested model with zero usage.
func (Client) Complete(
	_ context.Context,
	req types.CompletionRequest,
) (types.Completion, error) {
	return types.Completion{Text: Text, Model: req.Model}, nil
}
