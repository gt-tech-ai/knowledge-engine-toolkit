// Package docker builds the DockerClient from config: the stub (the default) or the
// docker binary run through a CommandRunner (exec), run through the client tier
// (Decorate).
package docker

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/docker/exec"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/docker/stub"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

const (
	// KindStub has no daemon (the default).
	KindStub client.Kind = "stub"

	// KindExec runs the docker binary.
	KindExec client.Kind = "exec"
)

// Config selects the docker client.
type Config struct {
	// Kind selects the backend; empty means KindStub.
	Kind client.Kind `yaml:"kind" mapstructure:"kind"`

	// Name labels the client in decoration overrides, metrics, spans and logs; empty
	// uses the family name ("docker").
	Name string `yaml:"name" mapstructure:"name"`
}

// Deps carries the docker client's collaborators.
type Deps struct {
	// Decorations decorates the client and supplies its observability.
	decorate.Decorations

	// Runner runs docker for KindExec.
	Runner interfaces.CommandRunner

	// Root is the directory docker runs in; build contexts and Compose files are
	// relative to it.
	Root string
}

// kinds is the docker backend table.
func kinds() client.Kinds[Config, Deps, interfaces.DockerClient] {
	return client.Kinds[Config, Deps, interfaces.DockerClient]{
		Family:   "docker",
		Default:  KindStub,
		Decorate: Decorate,
		Backends: map[client.Kind]client.Backend[Config, Deps, interfaces.DockerClient]{
			KindStub: func(context.Context, Config, Deps) (interfaces.DockerClient, error) {
				return stub.Client{}, nil
			},
			KindExec: newExec,
		},
	}
}

// newExec builds the exec backend; it needs a runner.
func newExec(_ context.Context, _ Config, deps Deps) (interfaces.DockerClient, error) {
	if deps.Runner == nil {
		return nil, apperr.New(apperr.CodeInvalidInput, "docker exec: no command runner")
	}
	return exec.New(deps.Runner, deps.Root), nil
}

// NewFromConfig builds the docker client cfg.Kind selects and runs it through the
// client tier. An unknown kind, or exec without a runner, is CodeInvalidInput.
func NewFromConfig(
	ctx context.Context,
	cfg Config,
	deps Deps,
) (interfaces.DockerClient, error) {
	return kinds().Build(ctx, cfg.Kind, cfg, deps, deps.Decorations, cfg.Name)
}
