package unit_test

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	platformgrpc "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc/grpc"
)

// TestGRPCServerFactory tests that the server factory creates a working gRPC server with
// a registered health service.
//
// Why this test is important:
//   - Kubernetes liveness and readiness probes depend on gRPC health checks
//   - A broken server factory prevents all gRPC services from starting
//   - Validates the full server lifecycle: create, register health, listen, serve, health
//     check responds
//
// What it tests:
//   - NewServer returns a non-nil gRPC server
//   - Health service responds with SERVING status via a connected client
func TestGRPCServerFactory(t *testing.T) {
	t.Parallel()

	cfg := platformgrpc.ServerConfig{
		MaxMessageSize: 16 * 1024 * 1024, // 16MB
	}

	server := platformgrpc.NewServer(cfg)
	require.NotNil(t, server, "expected non-nil gRPC server")

	// Use the package's RegisterHealthService helper
	platformgrpc.RegisterHealthService(server)

	// Start the server
	lis, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err, "failed to create listener")

	go func() {
		_ = server.Serve(lis)
	}()
	defer server.GracefulStop()

	// Connect a client and verify health check works
	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err, "failed to connect to server")
	defer func() { _ = conn.Close() }()

	client := healthpb.NewHealthClient(conn)
	resp, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	require.NoError(t, err, "health check failed")
	assert.Equal(t, healthpb.HealthCheckResponse_SERVING, resp.GetStatus())
}

// TestGRPCClientFactory tests that the client factory establishes a connection and can
// call the health service.
//
// Why this test is important:
//   - Validates end-to-end client-server connectivity to catch misconfigured defaults
//     early
//   - The client factory encapsulates connection options (timeouts, credentials,
//     interceptors)
//   - A broken client factory would prevent all inter-service gRPC communication
//
// What it tests:
//   - NewClient returns a non-nil connection without error
//   - Health check via the client connection returns SERVING status
func TestGRPCClientFactory(t *testing.T) {
	t.Parallel()

	// Start a test server to verify client connectivity
	cfg := platformgrpc.DefaultServerConfig()
	server := platformgrpc.NewServer(cfg)
	platformgrpc.RegisterHealthService(server)

	lis, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err, "failed to create listener")

	go func() {
		_ = server.Serve(lis)
	}()
	defer server.GracefulStop()

	// Use the package's client factory
	clientCfg := platformgrpc.DefaultClientConfig(lis.Addr().String())
	conn, err := platformgrpc.NewClient(clientCfg)
	require.NoError(t, err, "failed to create gRPC client")
	defer func() { _ = conn.Close() }()

	// Verify actual connectivity via health check
	client := healthpb.NewHealthClient(conn)
	resp, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	require.NoError(t, err, "health check via client factory failed")
	assert.Equal(t, healthpb.HealthCheckResponse_SERVING, resp.GetStatus())
}
