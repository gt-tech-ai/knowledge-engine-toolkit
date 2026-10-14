// Package server builds the ServerTransport from config: an unserved stub (the
// default) or Connect over net/http. The concrete backends live in the stub/ and
// connect/ subpackages; the contract is interfaces.ServerTransport.
package server

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/connect"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/server/stub"
)

const (
	// KindStub registers handlers on a mux it never serves (the default).
	KindStub = client.KindStub

	// KindConnect serves Connect RPC over net/http.
	KindConnect client.Kind = "connect"
)

// Config selects and tunes the server transport.
type Config struct {
	// Kind selects the backend; empty means KindStub.
	Kind client.Kind `yaml:"kind" mapstructure:"kind"`

	// Connect configures KindConnect.
	Connect connect.ServerConfig `yaml:"connect" mapstructure:"connect"`
}

// Deps carries the transport's runtime collaborators.
type Deps struct {
	// Logger reports server lifecycle events.
	Logger interfaces.Logger
}

// kinds is the transport backend table.
func kinds() client.Kinds[Config, Deps, interfaces.ServerTransport] {
	return client.Kinds[Config, Deps, interfaces.ServerTransport]{
		Family:  "transport",
		Default: KindStub,
		Backends: map[client.Kind]client.Backend[Config, Deps, interfaces.ServerTransport]{
			KindStub: func(context.Context, Config, Deps) (interfaces.ServerTransport, error) {
				return stub.New(), nil
			},
			KindConnect: func(
				_ context.Context, cfg Config, deps Deps,
			) (interfaces.ServerTransport, error) {
				return connect.NewServer(cfg.Connect, deps.Logger), nil
			},
		},
	}
}

// NewFromConfig builds the server transport cfg.Kind selects, without I/O. An unknown
// kind is CodeInvalidInput.
func NewFromConfig(
	ctx context.Context,
	cfg Config,
	deps Deps,
) (interfaces.ServerTransport, error) {
	return kinds().New(ctx, cfg.Kind, cfg, deps)
}
