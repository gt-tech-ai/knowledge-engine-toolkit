package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	grpcclient "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc/grpc"
)

// TestGRPCLifecycleClient_StartStopReadiness tests the gRPC lifecycle client's
// start/stop/health surface against a lazily-dialed (never-reachable) target.
//
// Why this test is important:
//   - This client backs a service's outbound RPC dependency; Liveness must pass once
//     the connection object exists while Readiness must fail once the connection is
//     shut down, so a torn-down dependency is reported unready to Kubernetes rather
//     than silently accepting traffic.
//
// What it tests:
//   - Start dials lazily (no error offline) and exposes a non-nil Conn.
//   - Liveness passes after Start; Stop closes the connection without error.
//   - Readiness fails after Stop (the connection is in a terminal Shutdown state).
func TestGRPCLifecycleClient_StartStopReadiness(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	c := grpcclient.NewLifecycleClient(grpcclient.ClientConfig{Target: "127.0.0.1:1"})
	require.NoError(
		t,
		c.Start(ctx),
		"grpc.NewClient is lazy, so Start does not block on connect",
	)
	require.NotNil(t, c.Conn(), "the dialed connection is exposed for building stubs")
	require.NoError(
		t,
		c.Liveness(ctx),
		"Liveness passes once the connection object exists",
	)

	require.NoError(t, c.Stop(ctx), "Stop closes the connection")
	require.Error(
		t,
		c.Readiness(ctx),
		"a shut-down connection is a terminal-failure state → not ready",
	)

	// An un-started client holds no connection, so Stop is a safe no-op.
	require.NoError(
		t,
		grpcclient.NewLifecycleClient(grpcclient.ClientConfig{Target: "127.0.0.1:1"}).
			Stop(ctx),
		"stopping a never-started client must be a no-op",
	)
}
