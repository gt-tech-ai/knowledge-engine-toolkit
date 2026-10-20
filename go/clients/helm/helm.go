// Package helm builds the HelmClient from config: the stub (the default) or the helm
// binary run through a CommandRunner (exec), run through the client tier (Decorate),
// and adapts a HelmClient to the Renderer a Codebase renders charts with.
package helm

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/helm/exec"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/helm/stub"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

const (
	// KindStub renders nothing (the default).
	KindStub client.Kind = "stub"

	// KindExec runs the helm binary.
	KindExec client.Kind = "exec"
)

// Config selects the helm client.
type Config struct {
	// Kind selects the backend; empty means KindStub.
	Kind client.Kind `yaml:"kind" mapstructure:"kind"`

	// Name labels the client in decoration overrides, metrics, spans and logs; empty
	// uses the family name ("helm").
	Name string `yaml:"name" mapstructure:"name"`
}

// Deps carries the helm client's collaborators.
type Deps struct {
	// Decorations decorates the client and supplies its observability.
	decorate.Decorations

	// Runner runs helm for KindExec.
	Runner interfaces.CommandRunner

	// Root is the directory helm runs in; chart and values paths are relative to it.
	Root string
}

// kinds is the helm backend table.
func kinds() client.Kinds[Config, Deps, interfaces.HelmClient] {
	return client.Kinds[Config, Deps, interfaces.HelmClient]{
		Family:   "helm",
		Default:  KindStub,
		Decorate: Decorate,
		Backends: map[client.Kind]client.Backend[Config, Deps, interfaces.HelmClient]{
			KindStub: func(context.Context, Config, Deps) (interfaces.HelmClient, error) {
				return stub.Client{}, nil
			},
			KindExec: func(_ context.Context, _ Config, deps Deps) (interfaces.HelmClient, error) {
				if deps.Runner == nil {
					return nil, apperr.New(
						apperr.CodeInvalidInput,
						"helm exec: no command runner",
					)
				}
				return exec.New(deps.Runner, deps.Root), nil
			},
		},
	}
}

// NewFromConfig builds the helm client cfg.Kind selects and runs it through the client
// tier. An unknown kind, or exec without a runner, is CodeInvalidInput.
func NewFromConfig(
	ctx context.Context,
	cfg Config,
	deps Deps,
) (interfaces.HelmClient, error) {
	return kinds().Build(ctx, cfg.Kind, cfg, deps, deps.Decorations, cfg.Name)
}
