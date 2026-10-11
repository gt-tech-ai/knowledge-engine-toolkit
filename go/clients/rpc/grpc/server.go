// Package grpc provides gRPC server and client factories.
package grpc

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
)

// ServerConfig holds configuration for a gRPC server.
type ServerConfig struct {
	// MaxMessageSize is the maximum allowed size in bytes for sent and received messages.
	MaxMessageSize int

	// KeepaliveTime is the interval between server keepalive pings to detect dead
	// connections.
	KeepaliveTime time.Duration

	// KeepaliveTimeout is the duration the server waits for a keepalive ping
	// acknowledgement.
	KeepaliveTimeout time.Duration

	// KeepaliveMinTime is the minimum interval the server allows between a client's
	// keepalive pings before it rejects the client with a GoAway (ENHANCE_YOUR_CALM).
	// It MUST be <= the client's keepalive Time: our grpc client (client.go) pings every
	// minute with PermitWithoutStream, but gRPC's default enforcement (MinTime 5m, no
	// pings without an active stream) is stricter than that and tears the connection
	// down with ENHANCE_YOUR_CALM. Relaxing it here keeps long-lived idle streams
	// stable.
	KeepaliveMinTime time.Duration
}

// DefaultServerConfig returns default gRPC server configuration.
func DefaultServerConfig() ServerConfig {
	return ServerConfig{
		MaxMessageSize:   16 * 1024 * 1024, // 16MB
		KeepaliveTime:    time.Minute,
		KeepaliveTimeout: 20 * time.Second,
		KeepaliveMinTime: 10 * time.Second,
	}
}

// NewServer creates a configured gRPC server.
// Interceptors should be provided via options.
func NewServer(cfg ServerConfig, opts ...grpc.ServerOption) *grpc.Server {
	serverOpts := make([]grpc.ServerOption, 0, 4+len(opts))
	serverOpts = append(serverOpts,
		grpc.MaxRecvMsgSize(cfg.MaxMessageSize),
		grpc.MaxSendMsgSize(cfg.MaxMessageSize),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    cfg.KeepaliveTime,
			Timeout: cfg.KeepaliveTimeout,
		}),
		// Accept the client's keepalive cadence (client.go pings every minute, even
		// with no active stream) instead of GoAway-ing it with ENHANCE_YOUR_CALM.
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             cfg.KeepaliveMinTime,
			PermitWithoutStream: true,
		}),
	)

	serverOpts = append(serverOpts, opts...)

	return grpc.NewServer(serverOpts...)
}

// RegisterHealthService registers the gRPC health check service.
func RegisterHealthService(server *grpc.Server) {
	grpc_health_v1.RegisterHealthServer(server, &healthServer{})
}

// healthServer implements the gRPC health check protocol.
type healthServer struct {
	// UnimplementedHealthServer provides forward-compatible default implementations.
	grpc_health_v1.UnimplementedHealthServer
}

// Check reports the serving status of the gRPC health protocol. It always reports
// SERVING; dependency readiness is not folded in.
func (h *healthServer) Check(
	_ context.Context,
	_ *grpc_health_v1.HealthCheckRequest,
) (*grpc_health_v1.HealthCheckResponse, error) {
	return &grpc_health_v1.HealthCheckResponse{
		Status: grpc_health_v1.HealthCheckResponse_SERVING,
	}, nil
}
