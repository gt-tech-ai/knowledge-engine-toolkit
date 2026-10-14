package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/database"
	dbpostgres "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/database/postgres"
	messagingsqs "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging/sqs"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc"
	grpcbackend "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc/grpc"
	storages3 "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/storage/s3"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
	connectpkg "github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/connect"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/server"
)

// TestDatabaseTier_NewFromConfig tests that the database tier selects the Postgres
// backend by Kind, and that the pool guards operations
// until it is started.
//
// Why this test is important:
//   - The tier factory is the app-wiring entrypoint; a mis-selected or nil pool, or
//     a pool that answers Ready before Start opens it, breaks every DB caller.
//
// What it tests:
//   - KindPostgres returns a non-nil DatabasePool.
//   - Before Start, DB() is nil and Liveness/Readiness report not-started.
func TestDatabaseTier_NewFromConfig(t *testing.T) {
	t.Parallel()

	pool, err := database.NewFromConfig(t.Context(), database.Config{
		Kind: database.KindPostgres,
	}, database.Deps{})
	require.NoError(t, err)
	require.NotNil(t, pool)
	assert.Nil(t, pool.DB(), "pool is not opened until Start")
	require.Error(t, pool.Liveness(context.Background()))
	require.Error(t, pool.Readiness(context.Background()))
	require.NoError(
		t,
		pool.Stop(context.Background()),
		"Stop on an unopened pool is a no-op",
	)
}

// TestRPCTier_NewFromConfig tests that the RPC tier selects the gRPC backend by
// Kind, and that the returned client's lifecycle behaves.
//
// Why this test is important:
//   - The RPC client is dialed on Start (lazily by gRPC); a client that reports
//     Ready before Start, or never exposes a Conn, cannot build working stubs.
//
// What it tests:
//   - KindGRPC returns a non-nil RPCClient.
//   - Before Start: Conn is nil and Liveness/Readiness report not-started.
//   - After Start: Conn is non-nil, Readiness passes (idle is not a failure state),
//     and Stop closes cleanly.
func TestRPCTier_NewFromConfig(t *testing.T) {
	t.Parallel()

	client, err := rpc.NewFromConfig(t.Context(), rpc.Config{
		Kind: rpc.KindGRPC,
		GRPC: grpcbackend.ClientConfig{Target: "localhost:1"},
	}, rpc.Deps{})
	require.NoError(t, err)
	require.NotNil(t, client)

	assert.Nil(t, client.Conn(), "not dialed until Start")
	require.Error(t, client.Liveness(context.Background()))
	require.Error(t, client.Readiness(context.Background()))

	require.NoError(t, client.Start(context.Background()))
	assert.NotNil(t, client.Conn())
	assert.NoError(t, client.Liveness(context.Background()))
	assert.NoError(t, client.Readiness(context.Background()))
	assert.NoError(t, client.Stop(context.Background()))
}

// TestTransportTier_NewFromConfig tests that the transport tier selects the Connect
// backend by Kind, and that the server reports not-ready
// until it is started.
//
// Why this test is important:
//   - The transport is the server's front door; a nil transport or one that reports
//     Ready before it is listening would make Kubernetes route traffic to a dead pod.
//
// What it tests:
//   - KindConnect returns a non-nil ServerTransport.
//   - Before Start, Readiness reports not-started while Liveness (process up) passes.
func TestTransportTier_NewFromConfig(t *testing.T) {
	t.Parallel()

	srv, err := server.NewFromConfig(t.Context(), server.Config{
		Kind:    server.KindConnect,
		Connect: connectpkg.DefaultServerConfig(0),
	}, server.Deps{Logger: fixtures.NopLogger()})
	require.NoError(t, err)
	require.NotNil(t, srv)
	require.NoError(t, srv.Liveness(context.Background()))
	require.Error(
		t,
		srv.Readiness(context.Background()),
		"not ready until Start binds the listener",
	)
	require.NoError(t, srv.Stop(context.Background()), "Stop before Start is a no-op")
}

// TestStatelessClients_LifecycleNoOps tests that the stateless SDK-backed clients
// (SQS, S3) satisfy interfaces.Lifecycle with safe no-op Start/Stop,
// so a lifecycle.Manager can register them uniformly with the connection-oriented
// clients.
//
// Why this test is important:
//   - These clients hold no persistent connection; their Start/Stop must be safe
//     no-ops so uniform registration never errors or blocks shutdown.
//
// What it tests:
//   - Each client's Start and Stop return nil.
func TestStatelessClients_LifecycleNoOps(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	pub, err := messagingsqs.NewPublisher(
		t.Context(),
		messagingsqs.Config{API: mocks.NewMockAPI(ctrl)},
	)
	require.NoError(t, err)

	s3Client := storages3.NewClientFromAPI(mocks.NewMockS3API(ctrl), 0)

	lifecycles := map[string]interfaces.Lifecycle{
		"sqs-publisher": pub,
		"s3-storage":    mustLifecycle(t, s3Client),
	}
	for name, lc := range lifecycles {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.NoError(t, lc.Start(ctx))
			assert.NoError(t, lc.Stop(ctx))
		})
	}
}

// mustLifecycle asserts v implements interfaces.Lifecycle and returns it, failing
// the test otherwise — the stateless clients are handed back as their domain
// contract, so the test surfaces the lifecycle seam through a type assertion.
func mustLifecycle(t *testing.T, v any) interfaces.Lifecycle {
	t.Helper()
	lc, ok := v.(interfaces.Lifecycle)
	require.True(t, ok, "%T does not implement interfaces.Lifecycle", v)
	return lc
}

// TestPostgresBackend_NotStartedGuards tests the Postgres backend's not-started
// guards directly (the tier test drives it through the interface; this pins the
// backend's own contract).
//
// Why this test is important:
//   - A pool that returns a usable DB() or passes Readiness before Start would let
//     callers issue queries against a nil handle and panic.
//
// What it tests:
//   - New (unopened): DB() is nil, Liveness and Readiness error, Stop is a no-op.
func TestPostgresBackend_NotStartedGuards(t *testing.T) {
	t.Parallel()

	c := dbpostgres.New(&infra.DatabaseConfig{})
	assert.Nil(t, c.DB())
	require.Error(t, c.Liveness(context.Background()))
	require.Error(t, c.Readiness(context.Background()))
	assert.NoError(t, c.Stop(context.Background()))
}

// TestPostgresBackend_StartUnreachable tests that Start surfaces an unreachable
// database as a boot-time error (rather than deferring the failure to the first
// query), by opening the pool and pinging.
//
// Why this test is important:
//   - Fail-fast at the composition root is the whole point of the pool's
//     Start; a Start that "succeeds" against a dead host hides the outage.
//
// What it tests:
//   - Start against an unresolvable host returns a non-nil error.
func TestPostgresBackend_StartUnreachable(t *testing.T) {
	t.Parallel()

	c := dbpostgres.New(&infra.DatabaseConfig{
		Host:     "invalid-host.example.invalid",
		Port:     5432,
		User:     "u",
		Password: "p",
		Database: "d",
		SSLMode:  "disable",
	})
	assert.Error(t, c.Start(context.Background()))
}
