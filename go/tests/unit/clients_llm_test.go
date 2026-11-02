package unit_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/llm"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/llm/stub"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/decoration"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// llmRequest is the request every backend test sends.
func llmRequest() types.CompletionRequest {
	return types.CompletionRequest{
		System:          "Answer briefly.",
		Messages:        []types.ChatMessage{{Role: types.RoleUser, Content: "Why?"}},
		Model:           "m-1",
		Temperature:     0.2,
		MaxOutputTokens: 64,
	}
}

// TestLLM_NewFromConfig_Kinds tests the factory's kind selection.
//
// Why this test is important:
//   - A composition root must boot with zero infrastructure, and a typo in the kind must
//     fail at boot rather than fall back silently.
//
// What it tests:
//   - An empty kind builds the stub, which returns its fixed text, the requested model
//     and zero usage; an unknown kind, ollama with no base URL and bedrock with no
//     Converse client are each CodeInvalidInput.
func TestLLM_NewFromConfig_Kinds(t *testing.T) {
	t.Parallel()
	c, err := llm.NewFromConfig(t.Context(), llm.Config{}, llm.Deps{})
	require.NoError(t, err)
	got, err := c.Complete(t.Context(), llmRequest())
	require.NoError(t, err)
	assert.Equal(t, types.Completion{Text: stub.Text, Model: "m-1"}, got)

	for name, cfg := range map[string]llm.Config{
		"unknown":         {Kind: "nope"},
		"ollama no url":   {Kind: llm.KindOllama},
		"bedrock no deps": {Kind: llm.KindBedrock},
	} {
		_, err := llm.NewFromConfig(t.Context(), cfg, llm.Deps{})
		require.Error(t, err, name)
		assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), name)
	}
}

// ollamaChat is the decoded body of one /api/chat request.
type ollamaChat struct {
	Options  map[string]float64 `json:"options"`
	Model    string             `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	Stream bool `json:"stream"`
}

// TestLLM_Ollama_MapsRequestAndUsage tests the ollama backend's wire mapping.
//
// Why this test is important:
//   - A dropped system prompt, a streamed reply or lost token counts would make every
//     answer and every cost figure wrong without an error.
//
// What it tests:
//   - One POST to <base>/api/chat, unstreamed, the system prompt first, temperature and
//     num_predict in options, num_ctx from ollama.context_tokens (Ollama silently
//     truncates a prompt longer than its context window); the reply's text, model and
//     eval counts map to Completion; an empty request model falls back to Config.Model.
func TestLLM_Ollama_MapsRequestAndUsage(t *testing.T) {
	t.Parallel()
	doer := mocks.NewMockHTTPDoer(gomock.NewController(t))
	var sent ollamaChat
	doer.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(r *http.Request) (*http.Response, error) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "http://ollama:11434/api/chat", r.URL.String())
			require.NoError(t, json.NewDecoder(r.Body).Decode(&sent))
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(
					`{"model":"m-1","message":{"role":"assistant","content":"Because."},` +
						`"done":true,"prompt_eval_count":12,"eval_count":3}`,
				)),
			}, nil
		})
	c, err := llm.NewFromConfig(t.Context(), llm.Config{
		Kind: llm.KindOllama, Model: "m-1",
		Ollama: llm.OllamaConfig{BaseURL: "http://ollama:11434/", ContextTokens: 16384},
	}, llm.Deps{Doer: doer})
	require.NoError(t, err)

	req := llmRequest()
	req.Model = ""
	got, err := c.Complete(t.Context(), req)
	require.NoError(t, err)

	assert.Equal(t, types.Completion{
		Text: "Because.", Model: "m-1", InputTokens: 12, OutputTokens: 3,
	}, got)
	assert.Equal(t, "m-1", sent.Model)
	assert.False(t, sent.Stream)
	require.Len(t, sent.Messages, 2)
	assert.Equal(t, "system", sent.Messages[0].Role)
	assert.Equal(t, "Answer briefly.", sent.Messages[0].Content)
	assert.Equal(t, "user", sent.Messages[1].Role)
	assert.InDelta(t, 0.2, sent.Options["temperature"], 1e-9)
	assert.InDelta(t, 64, sent.Options["num_predict"], 1e-9)
	assert.InDelta(t, 16384, sent.Options["num_ctx"], 1e-9)
}

// TestLLM_Ollama_ErrorCodes tests the ollama backend's error coding.
//
// Why this test is important:
//   - The client tier retries only transient codes: a rejected prompt must not be
//     replayed, and a down or overloaded server must be.
//
// What it tests:
//   - A transport failure and a 429, 500 or 503 are CodeUnavailable; a 400 or 404 is
//     CodeInvalidInput.
func TestLLM_Ollama_ErrorCodes(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		err    error
		want   apperr.ErrorCode
		status int
	}{
		"transport": {
			err:  apperr.Sentinel("connection refused"),
			want: apperr.CodeUnavailable,
		},
		"429": {status: http.StatusTooManyRequests, want: apperr.CodeUnavailable},
		"500": {
			status: http.StatusInternalServerError,
			want:   apperr.CodeUnavailable,
		},
		"503": {
			status: http.StatusServiceUnavailable,
			want:   apperr.CodeUnavailable,
		},
		"400": {status: http.StatusBadRequest, want: apperr.CodeInvalidInput},
		"404": {status: http.StatusNotFound, want: apperr.CodeInvalidInput},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			doer := mocks.NewMockHTTPDoer(gomock.NewController(t))
			doer.EXPECT().
				Do(gomock.Any()).
				DoAndReturn(func(*http.Request) (*http.Response, error) {
					if tc.err != nil {
						return nil, tc.err
					}
					return &http.Response{
						StatusCode: tc.status,
						Body:       io.NopCloser(strings.NewReader(`{"error":"x"}`)),
					}, nil
				})
			c, err := llm.NewFromConfig(t.Context(), llm.Config{
				Kind: llm.KindOllama, Ollama: llm.OllamaConfig{BaseURL: "http://o"},
			}, llm.Deps{Doer: doer})
			require.NoError(t, err)
			_, err = c.Complete(t.Context(), llmRequest())
			require.Error(t, err)
			assert.Equal(t, tc.want, apperr.Code(err))
		})
	}
}

// TestLLM_Bedrock_MapsConverseRequestAndUsage tests the bedrock backend's mapping.
//
// Why this test is important:
//   - The Converse API carries the system prompt and the inference settings in their own
//     fields; mapping them as messages, or dropping usage, changes answers and costs.
//
// What it tests:
//   - The model id, system block, user message, temperature and max tokens reach
//     Converse; the reply's text and token usage map to Completion.
func TestLLM_Bedrock_MapsConverseRequestAndUsage(t *testing.T) {
	t.Parallel()
	api := mocks.NewMockLLMConverseAPI(gomock.NewController(t))
	api.EXPECT().Converse(gomock.Any(), gomock.Any()).DoAndReturn(func(
		_ context.Context, in *bedrockruntime.ConverseInput, _ ...func(*bedrockruntime.Options),
	) (*bedrockruntime.ConverseOutput, error) {
		assert.Equal(t, "m-1", aws.ToString(in.ModelId))
		require.Len(t, in.System, 1)
		assert.Equal(t, "Answer briefly.",
			in.System[0].(*brtypes.SystemContentBlockMemberText).Value)
		require.Len(t, in.Messages, 1)
		assert.Equal(t, brtypes.ConversationRoleUser, in.Messages[0].Role)
		assert.InDelta(t, 0.2, aws.ToFloat32(in.InferenceConfig.Temperature), 1e-6)
		assert.Equal(t, int32(64), aws.ToInt32(in.InferenceConfig.MaxTokens))
		return &bedrockruntime.ConverseOutput{
			Output: &brtypes.ConverseOutputMemberMessage{Value: brtypes.Message{
				Role: brtypes.ConversationRoleAssistant,
				Content: []brtypes.ContentBlock{
					&brtypes.ContentBlockMemberText{Value: "Because."},
				},
			}},
			Usage: &brtypes.TokenUsage{
				InputTokens:  aws.Int32(12),
				OutputTokens: aws.Int32(3),
			},
		}, nil
	})
	c, err := llm.NewFromConfig(t.Context(), llm.Config{Kind: llm.KindBedrock},
		llm.Deps{Converse: api})
	require.NoError(t, err)
	got, err := c.Complete(t.Context(), llmRequest())
	require.NoError(t, err)
	assert.Equal(t, types.Completion{
		Text: "Because.", Model: "m-1", InputTokens: 12, OutputTokens: 3,
	}, got)
}

// TestLLM_Bedrock_ErrorCodes tests the bedrock backend's error coding.
//
// Why this test is important:
//   - A throttle must be retried by the client tier; a rejected request must not.
//
// What it tests:
//   - ThrottlingException is CodeUnavailable; ValidationException is CodeInvalidInput.
func TestLLM_Bedrock_ErrorCodes(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		err  error
		want apperr.ErrorCode
	}{
		"throttle": {err: &brtypes.ThrottlingException{}, want: apperr.CodeUnavailable},
		"validation": {
			err:  &brtypes.ValidationException{},
			want: apperr.CodeInvalidInput,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			api := mocks.NewMockLLMConverseAPI(gomock.NewController(t))
			api.EXPECT().Converse(gomock.Any(), gomock.Any()).Return(nil, tc.err)
			c, err := llm.NewFromConfig(t.Context(), llm.Config{Kind: llm.KindBedrock},
				llm.Deps{Converse: api})
			require.NoError(t, err)
			_, err = c.Complete(t.Context(), llmRequest())
			require.Error(t, err)
			assert.Equal(t, tc.want, apperr.Code(err))
		})
	}
}

// Compile-time check that the factory's kinds are client kinds.
var _ client.Kind = llm.KindStub

// TestLLM_Decorated_RetriesOnlyTransientCodes tests the llm client's default decoration.
//
// Why this test is important:
//   - A busy model server must be retried, but a rejected prompt replayed would only
//     spend money and open the breaker.
//
// What it tests:
//   - Under the default "llm" decoration (retry on, intervals shortened), a 503 then a
//     200 returns the reply after two sends; a 400 is sent once and is CodeInvalidInput.
func TestLLM_Decorated_RetriesOnlyTransientCodes(t *testing.T) {
	t.Parallel()
	layers := decoration.DefaultConfig().Clients.For("llm")
	layers.Retry.InitialInterval = time.Millisecond
	layers.Retry.MaxInterval = time.Millisecond
	deps, err := decorate.Build("llm", layers, decorate.Collaborators{})
	require.NoError(t, err)
	build := func(doer *mocks.MockHTTPDoer) interfaces.LLMClient {
		c, err := llm.NewFromConfig(t.Context(), llm.Config{
			Kind:   llm.KindOllama,
			Model:  "m-1",
			Ollama: llm.OllamaConfig{BaseURL: "http://o"},
		}, llm.Deps{Decorations: decorate.Fixed(deps), Doer: doer})
		require.NoError(t, err)
		return c
	}

	busy := mocks.NewMockHTTPDoer(gomock.NewController(t))
	gomock.InOrder(
		busy.EXPECT().
			Do(gomock.Any()).
			DoAndReturn(func(*http.Request) (*http.Response, error) {
				return promResponse(http.StatusServiceUnavailable, `{}`), nil
			}),
		busy.EXPECT().
			Do(gomock.Any()).
			DoAndReturn(func(*http.Request) (*http.Response, error) {
				return promResponse(http.StatusOK,
					`{"model":"m-1","message":{"role":"assistant","content":"ok"}}`), nil
			}),
	)
	got, err := build(busy).Complete(t.Context(), llmRequest())
	require.NoError(t, err)
	assert.Equal(t, "ok", got.Text)

	rejected := mocks.NewMockHTTPDoer(gomock.NewController(t))
	rejected.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(*http.Request) (*http.Response, error) {
			return promResponse(http.StatusBadRequest, `{}`), nil
		}).
		Times(1)
	_, err = build(rejected).Complete(t.Context(), llmRequest())
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}
