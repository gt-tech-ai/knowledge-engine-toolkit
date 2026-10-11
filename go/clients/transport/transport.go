// Package transport is the server-transport client tier: it selects an HTTP/RPC
// server backend — Connect over net/http today (connect/) — by Kind and returns the
// interfaces.ServerTransport contract, so switching server frameworks is a config
// change, not a caller edit. The contract lives in core (interfaces.ServerTransport);
// the backend implements it; this is the factory, mirroring the cache/storage
// NewFromConfig shape.
package transport

import (
	"fmt"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/transport/connect"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Kind selects the server-transport backend.
type Kind int

const (
	// KindConnect uses a Connect/gRPC server over net/http (production).
	KindConnect Kind = iota
)

// String returns the string form of Kind.
func (k Kind) String() string {
	switch k {
	case KindConnect:
		return "connect"
	default:
		return fmt.Sprintf("Kind(%d)", k)
	}
}

// Config selects and configures the server-transport backend.
type Config struct {
	// Logger is the structured logger passed to the server for lifecycle events.
	Logger interfaces.Logger

	// Connect holds the Connect server settings (port, timeouts, limits) used when
	// Kind==KindConnect.
	Connect connect.ServerConfig

	// Kind selects the backend. The zero value is KindConnect.
	Kind Kind
}

// NewFromConfig builds the server transport selected by cfg.Kind: the Connect
// server (KindConnect) today. It is the app-wiring entrypoint mirroring the
// cache/storage NewFromConfig factory; handlers are registered on the returned
// transport before Start.
func NewFromConfig(cfg Config) (interfaces.ServerTransport, error) {
	switch cfg.Kind {
	case KindConnect:
		return connect.NewServer(cfg.Connect, cfg.Logger), nil
	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("unknown transport kind: %v", cfg.Kind),
		)
	}
}
