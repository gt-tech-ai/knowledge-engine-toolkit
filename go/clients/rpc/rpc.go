// Package rpc is the RPC client tier: it selects an RPC transport backend — gRPC today
// (grpc/) — by Kind and returns the RPCClient contract, so switching RPC frameworks is a
// config change, not a caller edit. RPCClient is defined here rather than in core because
// it exposes grpc.ClientConnInterface — an SDK seam
// (ARCHITECTURE.md#interface-composition) — which keeps the gRPC dependency out of the
// dependency-free core layer. It mirrors the cache/storage NewFromConfig factory shape.
package rpc

import (
	"fmt"

	"google.golang.org/grpc"

	grpcbackend "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc/grpc"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// RPCClient is the RPC client contract: a lifecycle-managed, health-checkable client
// (interfaces.Client) that exposes the connection generated stubs are built on. It is an
// SDK seam (ARCHITECTURE.md#interface-composition) — Conn returns
// grpc.ClientConnInterface — so the contract lives in this tier, not core, to keep gRPC
// out of the core layer.
type RPCClient interface {
	// Client contributes the lifecycle-managed, health-checkable client surface.
	interfaces.Client

	// Conn returns the underlying connection for constructing generated gRPC stubs.
	// It is nil until Start dials the target.
	Conn() grpc.ClientConnInterface
}

// Kind selects the RPC transport backend.
type Kind int

const (
	// KindGRPC uses gRPC over HTTP/2 (production).
	KindGRPC Kind = iota
)

// String returns the string form of Kind.
func (k Kind) String() string {
	switch k {
	case KindGRPC:
		return "grpc"
	default:
		return fmt.Sprintf("Kind(%d)", k)
	}
}

// Config selects and configures the RPC backend.
type Config struct {
	// GRPC holds the gRPC client settings (target, message sizes, keepalive, TLS)
	// used when Kind==KindGRPC.
	GRPC grpcbackend.ClientConfig

	// Kind selects the backend. The zero value is KindGRPC.
	Kind Kind
}

// NewFromConfig builds the RPC client selected by cfg.Kind: the gRPC client
// (KindGRPC) today. It is the app-wiring entrypoint mirroring the cache/storage
// NewFromConfig factory; the connection is dialed on Start.
func NewFromConfig(cfg Config) (RPCClient, error) {
	switch cfg.Kind {
	case KindGRPC:
		return grpcbackend.NewLifecycleClient(cfg.GRPC), nil
	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("unknown rpc kind: %v", cfg.Kind),
		)
	}
}

// compile-time check: the gRPC backend satisfies the RPCClient contract.
var _ RPCClient = (*grpcbackend.Client)(nil)
