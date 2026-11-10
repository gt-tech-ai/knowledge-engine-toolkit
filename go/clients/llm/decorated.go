package llm

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Decorate runs every operation of base through the client tier as the client named
// name. Complete is retryable: a completion has no side effect but its cost, and the
// tier retries only transient codes, as the decoration config allows.
func Decorate(
	base interfaces.LLMClient,
	decs decorate.Decorations,
	name string,
	opts ...client.Option,
) (interfaces.LLMClient, error) {
	b, err := client.NewBase[interfaces.LLMClient](decs, name, opts...)
	if err != nil {
		return nil, err
	}

	return &decorator{base: base, Base: &b}, nil
}

// decorator runs each LLMClient operation through the client tier.
type decorator struct {
	// Base runs each operation through the client tier.
	*client.Base

	// base is the wrapped client.
	base interfaces.LLMClient
}

// Compile-time assertion that decorator satisfies LLMClient.
var _ interfaces.LLMClient = (*decorator)(nil)

// Complete runs the completion as the operation "complete".
func (d *decorator) Complete(
	ctx context.Context,
	req types.CompletionRequest,
) (types.Completion, error) {
	return d.Exec(ctx, "complete", func(ctx context.Context) (types.Completion, error) {
		return d.base.Complete(ctx, req)
	})
}
