package unit_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	grpcserver "github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/grpc"
)

const (
	// maxConnAgeShort is a short connection age, so the test observes the recycle fast.
	maxConnAgeShort = 150 * time.Millisecond

	// maxConnAgeDeadline bounds how long the test waits for the server to GOAWAY.
	maxConnAgeDeadline = 3 * time.Second
)

// TestNewServer_MaxConnectionAgeRecyclesConnection tests the server connection-age cap.
//
// Why this test is important:
//   - A client that round-robins over a headless Service only re-resolves DNS when a
//     connection ends. With MaxConnectionAge 0 (today's behaviour) a connection lives
//     until the pod dies, so pods added after a client connected stay idle. Capping the
//     age makes the server GOAWAY, so the client reconnects and picks up new pods
//
// What it tests:
//   - With a short MaxConnectionAge the server ends an established connection (the client
//     leaves READY within the deadline), and a later call still succeeds (the client
//     transparently reconnects).
func TestNewServer_MaxConnectionAgeRecyclesConnection(t *testing.T) {
	t.Parallel()

	cfg := grpcserver.DefaultServerConfig()

	cfg.MaxConnectionAge = maxConnAgeShort
	cfg.MaxConnectionAgeGrace = maxConnAgeShort

	srv := grpcserver.NewServer(cfg)
	grpcserver.RegisterHealthService(srv)

	lis, err := new(net.ListenConfig).Listen(t.Context(), textTCP, text127001)
	require.NoError(t, err)

	go func() { _ = srv.Serve(lis) }()

	t.Cleanup(srv.GracefulStop)

	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	client := healthpb.NewHealthClient(conn)

	_, err = client.Check(context.Background(), new(healthpb.HealthCheckRequest))
	require.NoError(t, err, "first call establishes the connection")

	require.Equal(t, connectivity.Ready, conn.GetState())

	ctx, cancel := context.WithTimeout(context.Background(), maxConnAgeDeadline)
	defer cancel()

	assert.True(t, conn.WaitForStateChange(ctx, connectivity.Ready),
		"the server should GOAWAY within the connection age, so the client leaves READY")

	_, err = client.Check(context.Background(), new(healthpb.HealthCheckRequest))
	assert.NoError(t, err, "the client reconnects transparently after the GOAWAY")
}
