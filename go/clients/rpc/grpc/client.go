package grpc

import (
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// ClientConfig holds configuration for a gRPC client.
type ClientConfig struct {
	// Target is the server address in the form host:port.
	Target string

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
		MaxMessageSize:   16 * 1024 * 1024, // 16MB
		KeepaliveTime:    time.Minute,
		KeepaliveTimeout: 20 * time.Second,
		UseTLS:           false, // insecure: enable TLS outside local dev
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
