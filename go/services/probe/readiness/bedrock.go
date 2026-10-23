package readiness

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	bedrockclient "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/bedrock"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Bedrock checks that each distinct model id is invokable, with a one-token Converse.
// It is the probe that makes a billed call, so it honors Target.NoInvoke.
type Bedrock struct {
	// Bedrock is the bedrock-runtime client.
	Bedrock bedrockclient.API

	// ModelIDs are the model ids to confirm; empty and repeated ids are dropped.
	ModelIDs []string
}

// Compile-time assertion that Bedrock is a readiness probe.
var _ interfaces.Probe[Target] = Bedrock{}

// Name is "bedrock".
func (Bedrock) Name() string { return "bedrock" }

// Run confirms each distinct id; with none it skips and with NoInvoke it warns
// Unverified. The first model that fails to converse fails the probe, naming it.
func (p Bedrock) Run(ctx context.Context, t Target) types.Verdict {
	var ids []string
	for _, id := range p.ModelIDs {
		if id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return skip("no bedrock model ids configured to confirm")
	}
	list := strconv.Itoa(len(ids)) + " model id(s)"
	if t.NoInvoke {
		return types.Verdict{Status: types.StatusWarn, Detail: Unverified +
			"--no-invoke: skipped the billed Converse for " + list + ": " +
			strings.Join(ids, ", ")}
	}
	for _, id := range ids {
		if _, err := p.Bedrock.Converse(ctx, converseInput(id)); err != nil {
			return fail(p.Name(), "model "+id+" not invokable: "+err.Error(), id)
		}
	}
	return pass("confirmed " + list + " with a 1-token Converse each: " +
		strings.Join(ids, ", "))
}

// converseInput is the smallest billed request: one user message, one output token.
func converseInput(modelID string) *bedrockruntime.ConverseInput {
	maxTokens := int32(1)
	return &bedrockruntime.ConverseInput{
		ModelId: aws.String(modelID),
		Messages: []brtypes.Message{
			{
				Role: brtypes.ConversationRoleUser,
				Content: []brtypes.ContentBlock{
					&brtypes.ContentBlockMemberText{Value: "ping"},
				},
			},
		},
		InferenceConfig: &brtypes.InferenceConfiguration{MaxTokens: &maxTokens},
	}
}
