// Package git builds the GitClient from config: the stub (the default) or the git
// binary run through a CommandRunner (exec), run through the client tier (Decorate).
package git

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/git/exec"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/git/stub"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

const (
	// KindStub touches nothing (the default).
	KindStub client.Kind = "stub"

	// KindExec runs the git binary.
	KindExec client.Kind = "exec"
)

// Config selects the git client.
type Config struct {
	// Kind selects the backend; empty means KindStub.
	Kind client.Kind `yaml:"kind" mapstructure:"kind"`

	// Name labels the client in decoration overrides, metrics, spans and logs; empty
	// uses the family name ("git").
	Name string `yaml:"name" mapstructure:"name"`
}

// Deps carries the git client's collaborators.
type Deps struct {
	// Decorations decorates the client and supplies its observability.
	decorate.Decorations

	// Runner runs git for KindExec.
	Runner interfaces.CommandRunner
}

// kinds is the git backend table.
func kinds() client.Kinds[Config, Deps, interfaces.GitClient] {
	return client.Kinds[Config, Deps, interfaces.GitClient]{
		Family:   "git",
		Default:  KindStub,
		Decorate: Decorate,
		Backends: map[client.Kind]client.Backend[Config, Deps, interfaces.GitClient]{
			KindStub: func(context.Context, Config, Deps) (interfaces.GitClient, error) {
				return stub.Client{}, nil
			},
			KindExec: newExec,
		},
	}
}

// newExec builds the exec backend; it needs a runner.
func newExec(_ context.Context, _ Config, deps Deps) (interfaces.GitClient, error) {
	if deps.Runner == nil {
		return nil, apperr.New(apperr.CodeInvalidInput, "git exec: no command runner")
	}
	return exec.New(deps.Runner), nil
}

// NewFromConfig builds the git client cfg.Kind selects and runs it through the client
// tier. An unknown kind, or exec without a runner, is CodeInvalidInput.
func NewFromConfig(
	ctx context.Context,
	cfg Config,
	deps Deps,
) (interfaces.GitClient, error) {
	return kinds().Build(ctx, cfg.Kind, cfg, deps, deps.Decorations, cfg.Name)
}
