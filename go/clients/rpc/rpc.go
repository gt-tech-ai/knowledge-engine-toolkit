// Package rpc builds the RPC client from config: gRPC (grpc/) today, behind RPCClient,
// so switching RPC frameworks is a config change. RPCClient lives here rather than in
// core because it exposes grpc.ClientConnInterface, an SDK seam
// (ARCHITECTURE.md#interface-composition) that keeps gRPC out of core. It ships no stub:
// a caller builds generated stubs on Conn, which nothing zero-I/O can stand in for, so
// an empty kind selects gRPC.
package rpc

import (
	"context"

	"google.golang.org/grpc"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	grpcbackend "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc/grpc"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc/grpc/interceptors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// RPCClient is the RPC client contract: a lifecycle-managed, health-checkable client
// (interfaces.Client) that exposes the connection generated stubs are built on.
//
// SDK seam — Conn returns grpc.ClientConnInterface, so the contract stays out of core.
type RPCClient interface {
	// Client contributes the lifecycle-managed, health-checkable client surface.
	interfaces.Client

	// Conn returns the underlying connection for constructing generated gRPC stubs.
	// It is nil until Start dials the target.
	Conn() grpc.ClientConnInterface
}

// KindGRPC dials a gRPC target (the default).
const KindGRPC client.Kind = "grpc"

// Config selects and tunes the RPC client.
type Config struct {
	// Kind selects the backend; empty means KindGRPC.
	Kind client.Kind `yaml:"kind" mapstructure:"kind"`

	// Name is the client's name in the decoration config's clients tier (and its metric,
	// span and log label); empty means "rpc".
	Name string `yaml:"name" mapstructure:"name"`

	// ServiceToken is the caller's service-to-service bearer token, sent on every call;
	// empty sends none (a local-dev server admits the call). Set it from secrets.
	ServiceToken string `yaml:"service_token" mapstructure:"service_token"`

	// GRPC configures KindGRPC.
	GRPC grpcbackend.ClientConfig `yaml:"grpc" mapstructure:"grpc"`
}

// Deps carries the RPC client's runtime collaborators.
type Deps struct {
	// Decorations builds the client's layers from the clients tier of the decoration
	// config, under Config.Name.
	decorate.Decorations
}

// kinds is the RPC backend table.
func kinds() client.Kinds[Config, Deps, RPCClient] {
	return client.Kinds[Config, Deps, RPCClient]{
		Family:  "rpc",
		Default: KindGRPC,
		Backends: map[client.Kind]client.Backend[Config, Deps, RPCClient]{
			KindGRPC: newGRPC,
		},
	}
}

// NewFromConfig builds the RPC client cfg.Kind selects, without I/O (it dials on
// Start), every call decorated by the clients tier under cfg.Name. An unknown kind, or a
// decoration that fails to build, is CodeInvalidInput.
func NewFromConfig(ctx context.Context, cfg Config, deps Deps) (RPCClient, error) {
	return kinds().New(ctx, cfg.Kind, cfg, deps)
}

// newGRPC builds the gRPC client, its calls run through the client tier (see
// interceptors.ClientOptions).
func newGRPC(_ context.Context, cfg Config, deps Deps) (RPCClient, error) {
	name := cfg.Name
	if name == "" {
		name = "rpc"
	}
	base, err := client.NewWireBase(deps.Decorations, name)
	if err != nil {
		return nil, err
	}
	opts := interceptors.ClientOptions(base, cfg.ServiceToken)
	return grpcbackend.NewLifecycleClient(cfg.GRPC, opts...), nil
}

var _ RPCClient = (*grpcbackend.Client)(nil)
