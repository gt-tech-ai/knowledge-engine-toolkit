// Package llm builds the LLMClient from config: the stub (the default, no I/O), Ollama's
// chat API or the Amazon Bedrock Converse API, run through the client tier (Decorate).
// The concrete backends live in the stub/, ollama/ and bedrock/ subpackages.
package llm

import (
	"context"
	"net/http"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/llm/bedrock"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/llm/ollama"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/llm/stub"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

const (
	// KindStub answers every request with a fixed text and zero usage (the default).
	KindStub = client.KindStub

	// KindOllama calls an Ollama server's chat API.
	KindOllama client.Kind = "ollama"

	// KindBedrock calls the Amazon Bedrock Converse API through Deps.Converse.
	KindBedrock client.Kind = "bedrock"
)

// DefaultOllamaTimeout is DefaultConfig's HTTP timeout for one Ollama request.
const DefaultOllamaTimeout = 120 * time.Second

// OllamaConfig configures KindOllama.
type OllamaConfig struct {
	// BaseURL is the server's root, e.g. http://localhost:11434; required.
	BaseURL string `yaml:"base_url" mapstructure:"base_url"`

	// Timeout bounds one HTTP request when Deps.Doer is nil; zero means no client-level
	// bound (the client tier's timeout still applies).
	Timeout time.Duration `yaml:"timeout" mapstructure:"timeout"`
}

// Config selects and configures the LLM client.
type Config struct {
	// Kind selects the backend; empty means KindStub.
	Kind client.Kind `yaml:"kind" mapstructure:"kind"`

	// Name labels the client in decoration overrides, metrics, spans and logs; empty
	// uses the family name ("llm").
	Name string `yaml:"name" mapstructure:"name"`

	// Model is the model a request that names none uses.
	Model string `yaml:"model" mapstructure:"model"`

	// Ollama configures KindOllama.
	Ollama OllamaConfig `yaml:"ollama" mapstructure:"ollama"`
}

// Deps carries the client's runtime collaborators.
type Deps struct {
	// Decorations decorates the client from the decoration config (decoration.clients)
	// and supplies its observability.
	decorate.Decorations

	// Doer replaces the http.Client KindOllama builds from Config.Ollama.Timeout.
	Doer interfaces.HTTPDoer

	// Converse is the Bedrock runtime client KindBedrock calls; required for it. Pass
	// the engine's Bedrock client (clients/aws/bedrock) so the call runs through its tier.
	Converse bedrock.ConverseAPI
}

// DefaultConfig returns the stub kind and a 120-second Ollama request timeout.
func DefaultConfig() Config {
	return Config{Ollama: OllamaConfig{Timeout: DefaultOllamaTimeout}}
}

// kinds is the LLM backend table.
func kinds() client.Kinds[Config, Deps, interfaces.LLMClient] {
	return client.Kinds[Config, Deps, interfaces.LLMClient]{
		Family:   "llm",
		Default:  KindStub,
		Decorate: Decorate,
		Backends: map[client.Kind]client.Backend[Config, Deps, interfaces.LLMClient]{
			KindStub: func(context.Context, Config, Deps) (interfaces.LLMClient, error) {
				return stub.New(), nil
			},
			KindOllama:  newOllama,
			KindBedrock: newBedrock,
		},
	}
}

// newOllama builds the Ollama backend; it needs a base URL.
func newOllama(_ context.Context, cfg Config, deps Deps) (interfaces.LLMClient, error) {
	if cfg.Ollama.BaseURL == "" {
		return nil, apperr.New(apperr.CodeInvalidInput,
			"llm: ollama.base_url is required for the ollama kind")
	}
	doer := deps.Doer
	if doer == nil {
		doer = &http.Client{Timeout: cfg.Ollama.Timeout}
	}
	return ollama.New(doer, cfg.Ollama.BaseURL, cfg.Model), nil
}

// newBedrock builds the Bedrock backend over Deps.Converse; it needs one.
func newBedrock(_ context.Context, cfg Config, deps Deps) (interfaces.LLMClient, error) {
	if deps.Converse == nil {
		return nil, apperr.New(apperr.CodeInvalidInput,
			"llm: the bedrock kind needs a Converse client (Deps.Converse)")
	}
	return bedrock.New(deps.Converse, cfg.Model), nil
}

// NewFromConfig builds the client cfg.Kind selects and runs it through the client tier.
// It makes no call. An unknown kind, ollama without a base URL and bedrock without a
// Converse client are CodeInvalidInput.
func NewFromConfig(
	ctx context.Context,
	cfg Config,
	deps Deps,
) (interfaces.LLMClient, error) {
	return kinds().Build(ctx, cfg.Kind, cfg, deps, deps.Decorations, cfg.Name)
}
