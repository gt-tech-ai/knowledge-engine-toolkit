// Package chaos builds the fault injector from config: the stub (the default), which
// injects nothing, or chaos-mesh, which creates and deletes Chaos Mesh custom resources
// with kubectl, run through the client tier (Decorate).
package chaos

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

const (
	// KindStub injects nothing and reports no live fault (the default).
	KindStub client.Kind = "stub"

	// KindChaosMesh injects faults as Chaos Mesh resources (PodChaos, NetworkChaos).
	KindChaosMesh client.Kind = "chaos-mesh"
)

// DefaultManagedBy is the managed-by label value when Config.ManagedBy is empty.
const DefaultManagedBy = "ke-chaos"

// Config selects and configures the injector.
type Config struct {
	// Kind selects the backend; empty means KindStub.
	Kind client.Kind `yaml:"kind" mapstructure:"kind"`

	// Name labels the injector in decoration overrides, metrics, spans and logs; empty
	// uses the family name ("chaos").
	Name string `yaml:"name" mapstructure:"name"`

	// ManagedBy is the app.kubernetes.io/managed-by value every resource the injector
	// creates carries and Active lists by; empty means DefaultManagedBy.
	ManagedBy string `yaml:"managed_by" mapstructure:"managed_by"`
}

// Deps carries the injector's collaborators.
type Deps struct {
	// Decorations decorates the injector and supplies its observability.
	decorate.Decorations

	// Runner runs kubectl (KindChaosMesh).
	Runner interfaces.CommandRunner

	// Root is the directory kubectl runs in.
	Root string
}

// kinds is the backend table.
func kinds() client.Kinds[Config, Deps, interfaces.FaultInjector] {
	return client.Kinds[Config, Deps, interfaces.FaultInjector]{
		Family:   "chaos",
		Default:  KindStub,
		Decorate: Decorate,
		Backends: map[client.Kind]client.Backend[Config, Deps, interfaces.FaultInjector]{
			KindStub: func(context.Context, Config, Deps) (interfaces.FaultInjector, error) {
				return Stub{}, nil
			},
			KindChaosMesh: newChaosMesh,
		},
	}
}

// NewFromConfig builds the injector cfg.Kind selects and runs it through the client
// tier; an unknown kind is CodeInvalidInput naming the known kinds. It does no I/O.
func NewFromConfig(
	ctx context.Context,
	cfg Config,
	deps Deps,
) (interfaces.FaultInjector, error) {
	return kinds().Build(ctx, cfg.Kind, cfg, deps, deps.Decorations, cfg.Name)
}

// Stub is the injector that injects nothing.
type Stub struct{}

// Compile-time assertion that Stub satisfies FaultInjector.
var _ interfaces.FaultInjector = Stub{}

// Inject does nothing.
func (Stub) Inject(context.Context, types.Fault) error { return nil }

// Revert does nothing.
func (Stub) Revert(context.Context, types.Fault) error { return nil }

// Active reports no live fault.
func (Stub) Active(context.Context, string) ([]string, error) { return nil, nil }
