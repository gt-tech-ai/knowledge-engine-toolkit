package unit_test

import (
	"context"
	"encoding/json"
	"net/http"
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
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/decoration"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// The request every backend test sends, and the reply the backends map.
const (
	// llmModel is the request's (and the config's default) model.
	llmModel = "llm-model-a"

	// llmSystem is the request's system prompt.
	llmSystem = "Answer briefly."

	// llmReply is the reply text every mapping test returns.
	llmReply = "Because."

	// llmTemperature is the request's sampling temperature.
	llmTemperature = 0.2

	// llmMaxTokens is the request's output-token cap.
	llmMaxTokens = 64

	// llmContextTokens is the Ollama context window the mapping test configures.
	llmContextTokens = 16384

	// llmInputTokens is the prompt's token count in the mapped replies.
	llmInputTokens = 12

	// llmOutputTokens is the reply's token count in the mapped replies.
	llmOutputTokens = 3

	// llmChatTurns is the turns an Ollama request carries: the system prompt and the user.
	llmChatTurns = 2

	// llmOllamaURL is the base URL the backend tests configure for KindOllama.
	llmOllamaURL = "http://ollama:11434/"

	// llmFloatDelta is the tolerance for a float the wire round-trips.
	llmFloatDelta = 1e-6
)

// llmRequest is the request every backend test sends.
func llmRequest() types.CompletionRequest {
	return types.CompletionRequest{
		System:          llmSystem,
		Messages:        []types.ChatMessage{{Role: types.RoleUser, Content: "Why?"}},
		Model:           llmModel,
		Temperature:     llmTemperature,
		MaxOutputTokens: llmMaxTokens,
	}
}

// llmMappedReply is the Completion a mapping test expects for llmReply.
func llmMappedReply() types.Completion {
	return types.Completion{
		Text:         llmReply,
		Model:        llmModel,
		InputTokens:  llmInputTokens,
		OutputTokens: llmOutputTokens,
	}
}

// llmConfig is a client config of kind with set applied to it.
func llmConfig(kind client.Kind, set func(*llm.Config)) *llm.Config {
	return fixtures.BuildRef(func(row *llm.Config) {
		row.Kind = kind
		set(row)
	})
}

// ollamaConfig is a KindOllama config over llmOllamaURL with llmModel as its default.
func ollamaConfig() *llm.Config {
	return llmConfig(llm.KindOllama, func(row *llm.Config) {
		row.Model = llmModel
		row.Ollama.BaseURL = llmOllamaURL
	})
}

// llmDeps is the client's deps with set applied to them.
func llmDeps(set func(*llm.Deps)) llm.Deps {
	return fixtures.Build(set)
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

	cfg := llm.DefaultConfig()

	llmClient, err := llm.NewFromConfig(t.Context(), &cfg, stdx.Zero[llm.Deps]())
	require.NoError(t, err)

	got, err := llmClient.Complete(t.Context(), llmRequest())
	require.NoError(t, err)
	assert.Equal(t, types.Completion{
		Text:         stub.Text,
		Model:        llmModel,
		InputTokens:  0,
		OutputTokens: 0,
	}, got)

	noop := func(*llm.Config) {}
	for name, bad := range map[string]*llm.Config{
		"unknown":         llmConfig("nope", noop),
		"ollama no url":   llmConfig(llm.KindOllama, noop),
		"bedrock no deps": llmConfig(llm.KindBedrock, noop),
	} {
		_, err := llm.NewFromConfig(t.Context(), bad, stdx.Zero[llm.Deps]())
		require.Error(t, err, name)
		assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), name)
	}
}

// ollamaTurn is one decoded chat turn of an /api/chat request.
type ollamaTurn struct {
	// Role is system, user or assistant.
	Role string `json:"role"`

	// Content is the turn's text.
	Content string `json:"content"`
}

// ollamaChat is the decoded body of one /api/chat request.
type ollamaChat struct {
	// Options carries temperature, num_predict and num_ctx.
	Options map[string]float64 `json:"options"`

	// Model names the model.
	Model string `json:"model"`

	// Messages are the turns, the system prompt first.
	Messages []ollamaTurn `json:"messages"`

	// Stream asks for a streamed reply.
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

			return promResponse(http.StatusOK,
				`{"model":"`+llmModel+`","message":{"role":"assistant","content":"`+
					llmReply+`"},"done":true,"prompt_eval_count":12,"eval_count":3}`), nil
		})

	cfg := ollamaConfig()

	cfg.Ollama.ContextTokens = llmContextTokens

	llmClient, err := llm.NewFromConfig(t.Context(), cfg, llmDeps(func(row *llm.Deps) {
		row.Doer = doer
	}))
	require.NoError(t, err)

	req := llmRequest()

	req.Model = ""

	got, err := llmClient.Complete(t.Context(), req)
	require.NoError(t, err)

	assert.Equal(t, llmMappedReply(), got)
	assert.Equal(t, llmModel, sent.Model)
	assert.False(t, sent.Stream)
	require.Len(t, sent.Messages, llmChatTurns)
	assert.Equal(t, ollamaTurn{Role: "system", Content: llmSystem}, sent.Messages[0])
	assert.Equal(t, "user", sent.Messages[1].Role)
	assert.InDelta(t, llmTemperature, sent.Options["temperature"], llmFloatDelta)
	assert.InDelta(t, llmMaxTokens, sent.Options["num_predict"], llmFloatDelta)
	assert.InDelta(t, llmContextTokens, sent.Options["num_ctx"], llmFloatDelta)
}

// ollamaFailing builds the ollama backend over a doer whose one send fails with err, or,
// when err is nil, answers status.
func ollamaFailing(t *testing.T, err error, status int) interfaces.LLMClient {
	t.Helper()

	doer := mocks.NewMockHTTPDoer(gomock.NewController(t))
	doer.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(*http.Request) (*http.Response, error) {
			if err != nil {
				return nil, err
			}

			return promResponse(status, `{"error":"x"}`), nil
		})

	llmClient, buildErr := llm.NewFromConfig(t.Context(), ollamaConfig(),
		llmDeps(func(row *llm.Deps) { row.Doer = doer }))
	require.NoError(t, buildErr)

	return llmClient
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
		"send fails": {
			err:    apperr.Sentinel("connection refused"),
			want:   apperr.CodeUnavailable,
			status: 0,
		},
		"429": {
			err:    nil,
			status: http.StatusTooManyRequests,
			want:   apperr.CodeUnavailable,
		},
		"500": {
			err:    nil,
			status: http.StatusInternalServerError,
			want:   apperr.CodeUnavailable,
		},
		"503": {
			err:    nil,
			status: http.StatusServiceUnavailable,
			want:   apperr.CodeUnavailable,
		},
		"400": {err: nil, status: http.StatusBadRequest, want: apperr.CodeInvalidInput},
		"404": {err: nil, status: http.StatusNotFound, want: apperr.CodeInvalidInput},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := ollamaFailing(t, testCase.err, testCase.status).
				Complete(t.Context(), llmRequest())
			require.Error(t, err)
			assert.Equal(t, testCase.want, apperr.Code(err))
		})
	}
}

// bedrockConfig is a KindBedrock config with llmModel as its default.
func bedrockConfig() *llm.Config {
	return llmConfig(llm.KindBedrock, func(row *llm.Config) { row.Model = llmModel })
}

// bedrockReply is the Converse output carrying llmReply and its token usage.
func bedrockReply() *bedrockruntime.ConverseOutput {
	return fixtures.BuildRef(func(row *bedrockruntime.ConverseOutput) {
		row.Output = &brtypes.ConverseOutputMemberMessage{Value: brtypes.Message{
			Role: brtypes.ConversationRoleAssistant,
			Content: []brtypes.ContentBlock{
				&brtypes.ContentBlockMemberText{Value: llmReply},
			},
		}}
		row.Usage = fixtures.BuildRef(func(usage *brtypes.TokenUsage) {
			usage.InputTokens = aws.Int32(llmInputTokens)
			usage.OutputTokens = aws.Int32(llmOutputTokens)
		})
	})
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
		_ context.Context,
		input *bedrockruntime.ConverseInput,
		_ ...func(*bedrockruntime.Options),
	) (*bedrockruntime.ConverseOutput, error) {
		assert.Equal(t, llmModel, aws.ToString(input.ModelId))
		require.Len(t, input.System, 1)

		system, isText := input.System[0].(*brtypes.SystemContentBlockMemberText)
		require.True(t, isText)
		assert.Equal(t, llmSystem, system.Value)
		require.Len(t, input.Messages, 1)
		assert.Equal(t, brtypes.ConversationRoleUser, input.Messages[0].Role)
		assert.InDelta(
			t,
			llmTemperature,
			aws.ToFloat32(input.InferenceConfig.Temperature),
			llmFloatDelta,
		)
		assert.Equal(t, int32(llmMaxTokens), aws.ToInt32(input.InferenceConfig.MaxTokens))

		return bedrockReply(), nil
	})

	llmClient, err := llm.NewFromConfig(t.Context(), bedrockConfig(),
		llmDeps(func(row *llm.Deps) { row.Converse = api }))
	require.NoError(t, err)

	got, err := llmClient.Complete(t.Context(), llmRequest())
	require.NoError(t, err)
	assert.Equal(t, llmMappedReply(), got)
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
		"throttle": {
			err:  new(brtypes.ThrottlingException),
			want: apperr.CodeUnavailable,
		},
		"validation": {
			err:  new(brtypes.ValidationException),
			want: apperr.CodeInvalidInput,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			api := mocks.NewMockLLMConverseAPI(gomock.NewController(t))
			api.EXPECT().Converse(gomock.Any(), gomock.Any()).Return(nil, testCase.err)

			llmClient, err := llm.NewFromConfig(t.Context(), bedrockConfig(),
				llmDeps(func(row *llm.Deps) { row.Converse = api }))
			require.NoError(t, err)

			_, err = llmClient.Complete(t.Context(), llmRequest())
			require.Error(t, err)
			assert.Equal(t, testCase.want, apperr.Code(err))
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

	cfg := decoration.DefaultConfig()
	layers := cfg.Clients.For("llm")

	layers.Retry.RetryConfig.InitialInterval = time.Millisecond
	layers.Retry.RetryConfig.MaxInterval = time.Millisecond

	deps, err := decorate.Build("llm", &layers, stdx.Zero[decorate.Collaborators]())
	require.NoError(t, err)

	build := func(doer *mocks.MockHTTPDoer) interfaces.LLMClient {
		llmClient, buildErr := llm.NewFromConfig(t.Context(), ollamaConfig(),
			llmDeps(func(row *llm.Deps) {
				row.Decorations = decorate.Fixed(&deps)
				row.Doer = doer
			}))
		require.NoError(t, buildErr)

		return llmClient
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
				return promResponse(
					http.StatusOK,
					`{"model":"`+llmModel+`","message":{"role":"assistant","content":"ok"}}`,
				), nil
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
