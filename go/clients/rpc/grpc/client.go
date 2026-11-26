// Package grpc dials gRPC client connections: NewClient builds one, and Client manages
// it as a lifecycle-managed, health-checkable client (interfaces.Client).
package grpc

import (
	"strings"
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

	// ResolveInterval, when positive and LoadBalancing is LoadBalancingRoundRobin,
	// re-resolves the target's DNS address set on this interval so a round_robin client
	// dialing a headless Kubernetes Service picks up pods added by a scale-up without
	// waiting for a connection to break. The zero value disables periodic re-resolution
	// (gRPC's dns resolver re-resolves only on a connection failure) and leaves the target
	// untouched. It has no effect with pick_first.
	ResolveInterval time.Duration
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
		ResolveInterval:  0, // periodic re-resolution off; opt in with round_robin
	}
}

// dnsEndpoint returns the host:port endpoint of a gRPC target, stripping a leading
// "dns:///" scheme so a round_robin target written as dns:///host:port can be re-dialed
// under DNSPollScheme. A bare host:port is returned unchanged.
func dnsEndpoint(target string) string {
	const dnsPrefix = "dns:///"

	return strings.TrimPrefix(target, dnsPrefix)
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

	// Periodic re-resolution only helps round_robin (pick_first re-resolves anyway on
	// reconnect), so it is opt-in there via a positive ResolveInterval: dial the target
	// under DNSPollScheme with a polling resolver that re-runs the lookup on the interval.
	target := cfg.Target
	if cfg.LoadBalancing == LoadBalancingRoundRobin && cfg.ResolveInterval > 0 {
		dialOpts = append(dialOpts,
			grpc.WithResolvers(NewDNSPollingResolver(cfg.ResolveInterval, nil)))
		target = DNSPollScheme + ":///" + dnsEndpoint(cfg.Target)
	}

	conn, err := grpc.NewClient(target, dialOpts...)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "create grpc client")
	}

	return conn, nil
}
