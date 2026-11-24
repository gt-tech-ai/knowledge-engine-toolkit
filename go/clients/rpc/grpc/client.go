// Package grpc dials gRPC client connections: NewClient builds one, and Client manages
// it as a lifecycle-managed, health-checkable client (interfaces.Client).
package grpc

import (
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
)

// defaultMaxMessageSize is the default limit on one gRPC message, in bytes.
const defaultMaxMessageSize = 16 * 1024 * 1024

// defaultClientConfigKeepaliveTimeout is the default KeepaliveTimeout of ClientConfig.
const defaultClientConfigKeepaliveTimeout = 20 * time.Second

// LoadBalancing names the client-side gRPC load-balancing policy.
type LoadBalancing string

const (
	// LoadBalancingPickFirst is gRPC's default: one subchannel to the first resolved
	// address, so every call rides one connection. It is the zero value.
	LoadBalancingPickFirst LoadBalancing = "pick_first"

	// LoadBalancingRoundRobin opens a subchannel to every resolved address and rotates
	// calls across them, so a service scaled behind a headless Service spreads load over
	// its pods.
	LoadBalancingRoundRobin LoadBalancing = "round_robin"
)

// roundRobinServiceConfig is the default service config that selects the round_robin
// balancer for every method on the connection.
const roundRobinServiceConfig = `{"loadBalancingConfig":[{"round_robin":{}}]}`

// ClientConfig holds configuration for a gRPC client.
type ClientConfig struct {
	// Target is the server address in the form host:port.
	Target string

	// LoadBalancing selects the client-side load-balancing policy; the zero value
	// (empty) and LoadBalancingPickFirst both mean pick_first.
	LoadBalancing LoadBalancing

	// MaxMessageSize is the maximum allowed size in bytes for sent and received messages.
	MaxMessageSize int

	// KeepaliveTime is the interval between client keepalive pings to detect dead
	// connections.
	KeepaliveTime time.Duration

	// KeepaliveTimeout is the duration the client waits for a keepalive ping
	// acknowledgement.
	KeepaliveTimeout time.Duration

	// UseTLS enables TLS transport credentials; when false, insecure credentials are used.
	UseTLS bool
}

// DefaultClientConfig returns default gRPC client configuration.
func DefaultClientConfig(target string) ClientConfig {
	return ClientConfig{
		Target:           target,
		MaxMessageSize:   defaultMaxMessageSize, // 16MB
		KeepaliveTime:    time.Minute,
		KeepaliveTimeout: defaultClientConfigKeepaliveTimeout,
		UseTLS:           false, // insecure: enable TLS outside local dev
		LoadBalancing:    LoadBalancingPickFirst,
	}
}

// NewClient creates a configured gRPC client connection.
// Interceptors should be provided via options.
func NewClient(cfg ClientConfig, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	dialOpts := []grpc.DialOption{
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(cfg.MaxMessageSize),
			grpc.MaxCallSendMsgSize(cfg.MaxMessageSize),
		),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                cfg.KeepaliveTime,
			Timeout:             cfg.KeepaliveTimeout,
			PermitWithoutStream: true,
		}),
	}

	switch cfg.LoadBalancing {
	case "", LoadBalancingPickFirst:
		// gRPC's default; no service config needed.
	case LoadBalancingRoundRobin:
		dialOpts = append(
			dialOpts,
			grpc.WithDefaultServiceConfig(roundRobinServiceConfig),
		)
	default:
		return nil, apperr.New(apperr.CodeInvalidInput,
			"grpc client: unknown load_balancing "+string(cfg.LoadBalancing))
	}

	if !cfg.UseTLS {
		dialOpts = append(
			dialOpts,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
	}

	dialOpts = append(dialOpts, opts...)

	conn, err := grpc.NewClient(cfg.Target, dialOpts...)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "create grpc client")
	}

	return conn, nil
}
