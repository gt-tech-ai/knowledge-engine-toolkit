package unit_test

import (
	"context"
	"net"
	"testing"

	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc"
	platformgrpc "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc/grpc"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/decoration"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/metrics/prom"
	grpcserver "github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/grpc"
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

	cfg := grpcserver.ServerConfig{
		MaxMessageSize: 16 * 1024 * 1024, // 16MB
	}

	server := grpcserver.NewServer(cfg)
	require.NotNil(t, server, "expected non-nil gRPC server")

	// Use the package's RegisterHealthService helper
	grpcserver.RegisterHealthService(server)

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
	cfg := grpcserver.DefaultServerConfig()
	server := grpcserver.NewServer(cfg)
	grpcserver.RegisterHealthService(server)

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

// TestRPCNewFromConfig_DecoratesEveryCall tests that the RPC factory installs the client
// tier on the connection it builds.
//
// Why this test is important:
//   - An RPC client built from config must be decorated like every other client — its
//     calls metered, traced and resilient as the decoration config says — without the
//     composition root assembling interceptors by hand; a factory that skipped the
//     chain would leave inter-service calls unobserved.
//
// What it tests:
//   - A health check through a client from rpc.NewFromConfig against a live server
//     succeeds and is counted once in client_operations_total under the operation
//     "grpc.health.v1.Health/Check".
func TestRPCNewFromConfig_DecoratesEveryCall(t *testing.T) {
	t.Parallel()
	server := grpcserver.NewServer(grpcserver.DefaultServerConfig())
	grpcserver.RegisterHealthService(server)
	lis, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(server.GracefulStop)

	reg := promclient.NewRegistry()
	defaults := decoration.DefaultConfig()
	rpcClient, err := rpc.NewFromConfig(t.Context(), rpc.Config{
		Name: "health-probe",
		GRPC: platformgrpc.DefaultClientConfig(lis.Addr().String()),
	}, rpc.Deps{Decorations: decorate.NewDecorations(&defaults, decorate.Collaborators{
		Observability: decorate.Observability{Metrics: prom.NewFromRegistry(reg)},
	})})
	require.NoError(t, err)
	require.NoError(t, rpcClient.Start(t.Context()))
	t.Cleanup(func() { _ = rpcClient.Stop(context.Background()) })

	resp, err := healthpb.NewHealthClient(rpcClient.Conn()).
		Check(t.Context(), &healthpb.HealthCheckRequest{})
	require.NoError(t, err)
	assert.Equal(t, healthpb.HealthCheckResponse_SERVING, resp.GetStatus())
	assert.InDelta(t, 1, clientOpCounter(t, reg, "grpc.health.v1.Health/Check"), 0)
}
