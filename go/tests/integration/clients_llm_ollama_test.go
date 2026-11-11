//go:build integration

package integration_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/llm"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	ollamadb "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/dbtest/ollama"
	testsuite "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/suite"
)

// replyTokenCap caps the one-word reply the suite asks for.
const replyTokenCap = 16

// LLMOllamaSuite runs the ollama kind of the LLM client against a real Ollama server.
type LLMOllamaSuite struct {
	testsuite.OllamaIntegrationSuite
}

// TestLLMOllamaSuite is the testify entrypoint for the Ollama suite.
//
// Why this test is important:
//   - Only a real server proves the chat endpoint, the unstreamed reply and the token
//     counts the client maps are what Ollama actually sends.
//
// What it tests:
//   - Wires LLMOllamaSuite into the runner.
func TestLLMOllamaSuite(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(LLMOllamaSuite))
}

// TestComplete_AnswersOnePrompt sends one prompt through the factory-built client.
//
// Why this test is important:
//   - This is the wire-level proof the ollama kind answers: request shape, endpoint,
//     reply decoding and usage, through the client tier.
//
// What it tests:
//   - A one-line prompt returns non-empty text from the fixture's model, with non-zero
//     input and output token counts.
func (s *LLMOllamaSuite) TestComplete_AnswersOnePrompt() {
	cfg := llm.DefaultConfig()

	cfg.Kind = llm.KindOllama
	cfg.Model = ollamadb.Model
	cfg.Ollama.BaseURL = s.OllamaBaseURL

	client, err := llm.NewFromConfig(context.Background(), &cfg, stdx.Zero[llm.Deps]())
	s.Require().NoError(err)

	got, err := client.Complete(context.Background(), types.CompletionRequest{
		System: "Reply with one word.",
		Messages: []types.ChatMessage{
			{Role: types.RoleUser, Content: "Say hello."},
		},
		MaxOutputTokens: replyTokenCap,
		Model:           "",
		Temperature:     0,
	})
	s.Require().NoError(err)
	s.NotEmpty(strings.TrimSpace(got.Text))
	s.Equal(ollamadb.Model, got.Model)
	s.Positive(got.InputTokens)
	s.Positive(got.OutputTokens)
}
