// Package bedrock is the LLMClient over the Amazon Bedrock Converse API. It calls
// through an injected ConverseAPI: the composition root passes the engine's Bedrock
// runtime client (clients/aws/bedrock), so the call runs through that client's tier too.
// Errors are coded by foundation/awserr: a throttle or server fault is CodeUnavailable
// (retried), a rejected request CodeInvalidInput.
package bedrock

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/awserr"
)

// ConverseAPI is the one Bedrock runtime call the backend makes, with the SDK client's
// own signature, so the SDK client and the engine's decorated Bedrock client both
// satisfy it.
//
// SDK seam — wraps the aws-sdk-go-v2 Bedrock runtime Converse call; unit tests inject a
// generated mock.
type ConverseAPI interface {
	// Converse sends messages to a model and returns its reply.
	Converse(
		ctx context.Context,
		in *bedrockruntime.ConverseInput,
		optFns ...func(*bedrockruntime.Options),
	) (*bedrockruntime.ConverseOutput, error)
}

// Client is the Bedrock LLMClient.
type Client struct {
	// api makes the Converse call.
	api ConverseAPI

	// model is the model id a request with none uses.
	model string
}

// Compile-time assertion that Client satisfies LLMClient.
var _ interfaces.LLMClient = (*Client)(nil)

// New returns the client over api with model as the default model id.
func New(api ConverseAPI, model string) *Client { return &Client{api: api, model: model} }

// Complete sends req through Converse and maps the reply's text and token usage.
func (c *Client) Complete(
	ctx context.Context,
	req types.CompletionRequest,
) (types.Completion, error) {
	model := req.Model
	if model == "" {
		model = c.model
	}
	out, err := c.api.Converse(ctx, converseInput(model, req))
	if err != nil {
		return types.Completion{}, awserr.Classify(err)
	}
	msg, ok := out.Output.(*brtypes.ConverseOutputMemberMessage)
	if !ok {
		return types.Completion{}, apperr.New(apperr.CodeUpstream,
			"bedrock converse: reply carries no message")
	}
	var text strings.Builder
	for _, block := range msg.Value.Content {
		if t, ok := block.(*brtypes.ContentBlockMemberText); ok {
			text.WriteString(t.Value)
		}
	}
	got := types.Completion{Text: text.String(), Model: model}
	if out.Usage != nil {
		got.InputTokens = int(aws.ToInt32(out.Usage.InputTokens))
		got.OutputTokens = int(aws.ToInt32(out.Usage.OutputTokens))
	}
	return got, nil
}

// converseInput maps req to the Converse request: the system prompt in its own field,
// each turn a text message, temperature and the token cap in the inference config.
func converseInput(
	model string,
	req types.CompletionRequest,
) *bedrockruntime.ConverseInput {
	in := &bedrockruntime.ConverseInput{
		ModelId: aws.String(model),
		InferenceConfig: &brtypes.InferenceConfiguration{
			Temperature: aws.Float32(float32(req.Temperature)),
		},
	}
	if req.MaxOutputTokens > 0 {
		in.InferenceConfig.MaxTokens = aws.Int32(int32(min(req.MaxOutputTokens, 1<<31-1)))
	}
	if req.System != "" {
		in.System = []brtypes.SystemContentBlock{
			&brtypes.SystemContentBlockMemberText{Value: req.System},
		}
	}
	for _, m := range req.Messages {
		role := brtypes.ConversationRoleUser
		if m.Role == types.RoleAssistant {
			role = brtypes.ConversationRoleAssistant
		}
		in.Messages = append(in.Messages, brtypes.Message{
			Role: role,
			Content: []brtypes.ContentBlock{
				&brtypes.ContentBlockMemberText{Value: m.Content},
			},
		})
	}
	return in
}
