package interfaces

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// LLMClient asks a language model for a chat completion.
//
// Implementations: the stub (a fixed reply, no I/O), Ollama and Amazon Bedrock
// (clients/llm). A backend codes its failures: an unreachable or overloaded backend is
// CodeUnavailable (transient), a request the backend rejects CodeInvalidInput.
type LLMClient interface {
	// Complete sends req and returns the model's reply with its token usage.
	Complete(ctx context.Context, req types.CompletionRequest) (types.Completion, error)
}
