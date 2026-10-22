// Package kubectl builds the KubectlClient from config: the stub (the default) or the
// kubectl binary run through a CommandRunner (exec), run through the client tier
// (Decorate).
package kubectl

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/kubectl/exec"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/kubectl/stub"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

const (
	// KindStub has no cluster (the default).
	KindStub client.Kind = "stub"

	// KindExec runs the kubectl binary.
	KindExec client.Kind = "exec"
)

// Config selects the kubectl client.
type Config struct {
	// Kind selects the backend; empty means KindStub.
	Kind client.Kind `yaml:"kind" mapstructure:"kind"`

	// Name labels the client in decoration overrides, metrics, spans and logs; empty
	// uses the family name ("kubectl").
	Name string `yaml:"name" mapstructure:"name"`

	// Context pins every call to this kubeconfig context; empty uses the current one.
	Context string `yaml:"context" mapstructure:"context"`
}

// Deps carries the kubectl client's collaborators.
type Deps struct {
	// Decorations decorates the client and supplies its observability.
	decorate.Decorations

	// Runner runs kubectl for KindExec.
	Runner interfaces.CommandRunner
}

// kinds is the kubectl backend table.
func kinds() client.Kinds[Config, Deps, interfaces.KubectlClient] {
	return client.Kinds[Config, Deps, interfaces.KubectlClient]{
		Family:   "kubectl",
		Default:  KindStub,
		Decorate: Decorate,
		Backends: map[client.Kind]client.Backend[Config, Deps, interfaces.KubectlClient]{
			KindStub: func(context.Context, Config, Deps) (interfaces.KubectlClient, error) {
				return stub.Client{}, nil
			},
			KindExec: newExec,
		},
	}
}

// newExec builds the exec backend; it needs a runner.
func newExec(_ context.Context, cfg Config, deps Deps) (interfaces.KubectlClient, error) {
	if deps.Runner == nil {
		return nil, apperr.New(apperr.CodeInvalidInput, "kubectl exec: no command runner")
	}
	return exec.New(deps.Runner, cfg.Context), nil
}

// NewFromConfig builds the kubectl client cfg.Kind selects and runs it through the
// client tier. An unknown kind, or exec without a runner, is CodeInvalidInput.
func NewFromConfig(
	ctx context.Context,
	cfg Config,
	deps Deps,
) (interfaces.KubectlClient, error) {
	return kinds().Build(ctx, cfg.Kind, cfg, deps, deps.Decorations, cfg.Name)
}
