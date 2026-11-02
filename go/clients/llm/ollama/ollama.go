// Package ollama is the LLMClient over the Ollama chat API (POST <base>/api/chat,
// unstreamed). Errors are coded for the client tier: a transport failure or a 429/5xx
// is CodeUnavailable (retried), any other non-2xx CodeInvalidInput.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/httperr"
)

// maxErrorBody caps how much of a failed response's body an error quotes.
const maxErrorBody = 512

// Client is the Ollama LLMClient.
type Client struct {
	// doer sends each request.
	doer interfaces.HTTPDoer

	// url is the chat endpoint, <base>/api/chat.
	url string

	// model is the model a request with none uses.
	model string

	// numCtx is the context window sent as num_ctx; zero sends none.
	numCtx int
}

// Compile-time assertion that Client satisfies LLMClient.
var _ interfaces.LLMClient = (*Client)(nil)

// New returns the client for the server at baseURL (a trailing slash is ignored),
// sending through doer, with model as the default model and numCtx (zero: the
// server's default) as the context window.
func New(doer interfaces.HTTPDoer, baseURL, model string, numCtx int) *Client {
	return &Client{
		doer:   doer,
		url:    strings.TrimRight(baseURL, "/") + "/api/chat",
		model:  model,
		numCtx: numCtx,
	}
}

// message is one chat turn on the wire.
type message struct {
	// Role is system, user or assistant.
	Role string `json:"role"`

	// Content is the turn's text.
	Content string `json:"content"`
}

// chatRequest is the /api/chat request body.
type chatRequest struct {
	// Options carries temperature and num_predict.
	Options map[string]any `json:"options,omitempty"`

	// Model names the model.
	Model string `json:"model"`

	// Messages are the turns, the system prompt first.
	Messages []message `json:"messages"`

	// Stream is always false: one reply object.
	Stream bool `json:"stream"`
}

// chatResponse is the unstreamed /api/chat reply.
type chatResponse struct {
	// Model is the model that answered.
	Model string `json:"model"`

	// Message is the reply.
	Message message `json:"message"`

	// PromptEvalCount is the prompt's token count.
	PromptEvalCount int `json:"prompt_eval_count"`

	// EvalCount is the reply's token count.
	EvalCount int `json:"eval_count"`
}

// Complete sends req to /api/chat and maps the reply and its token counts.
func (c *Client) Complete(
	ctx context.Context,
	req types.CompletionRequest,
) (types.Completion, error) {
	body, err := json.Marshal(c.wire(req))
	if err != nil {
		return types.Completion{}, apperr.Wrap(
			err,
			apperr.CodeInternal,
			"ollama: encode request",
		)
	}
	httpReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.url,
		bytes.NewReader(body),
	)
	if err != nil {
		return types.Completion{}, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"ollama: build request",
		)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.doer.Do(httpReq)
	if err != nil {
		return types.Completion{}, httperr.Transport(err, "ollama: request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return types.Completion{}, statusError(resp)
	}
	var out chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return types.Completion{}, apperr.Wrap(
			err,
			apperr.CodeUnavailable,
			"ollama: decode reply",
		)
	}
	return types.Completion{
		Text:         out.Message.Content,
		Model:        out.Model,
		InputTokens:  out.PromptEvalCount,
		OutputTokens: out.EvalCount,
	}, nil
}

// wire maps req to the request body: the system prompt as the first turn, and the
// default model when req names none.
func (c *Client) wire(req types.CompletionRequest) chatRequest {
	model := req.Model
	if model == "" {
		model = c.model
	}
	msgs := make([]message, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, message{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, message{Role: string(m.Role), Content: m.Content})
	}
	opts := map[string]any{"temperature": req.Temperature}
	if req.MaxOutputTokens > 0 {
		opts["num_predict"] = req.MaxOutputTokens
	}
	if c.numCtx > 0 {
		opts["num_ctx"] = c.numCtx
	}
	return chatRequest{Model: model, Messages: msgs, Options: opts}
}

// statusError codes a non-2xx reply: 429 and 5xx are CodeUnavailable, anything else
// CodeInvalidInput; the message quotes the start of the body.
func statusError(resp *http.Response) error {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	msg := "ollama: status " + strconv.Itoa(resp.StatusCode) + ": " +
		strings.TrimSpace(string(snippet))
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return apperr.New(apperr.CodeUnavailable, msg)
	}
	return apperr.New(apperr.CodeInvalidInput, msg)
}
