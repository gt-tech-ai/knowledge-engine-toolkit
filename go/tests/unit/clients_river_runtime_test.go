package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	riverpkg "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/jobs/river"
)

// unreachableDB is a well-formed DSN whose port refuses connections instantly, so
// the lazy pgxpool opens without error but any actual query fails fast (mirroring
// the postgres client tests).
const unreachableDB = "postgres://u:p@127.0.0.1:1/db?sslmode=disable"

// TestRiverRuntime_NewRuntime_ConfigParsing tests that NewRuntime parses the pool
// DSN, applies the connection/worker caps, and constructs the River client offline.
//
// Why this test is important:
//   - NewRuntime is the composition-root constructor for a River worker
//     runtime; a DSN-parse or client-construction regression stops every replica from
//     booting, and it must fail loudly at construction rather than at first job.
//
// What it tests:
//   - A malformed DSN (bad pool_max_conns) returns a wrapped parse error.
//   - A nil Workers bundle (with the default queue always configured) is rejected by
//     the River client with a wrapped error.
//   - A valid DSN with an empty Workers bundle and explicit caps constructs a runtime
//     whose Client() and pool are live (the pgxpool connects lazily, so no DB needed).
func TestRiverRuntime_NewRuntime_ConfigParsing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("malformed dsn returns parse error", func(t *testing.T) {
		t.Parallel()
		_, err := riverpkg.NewRuntime(ctx, riverpkg.RuntimeConfig{
			Workers:     river.NewWorkers(),
			DatabaseURL: "postgres://u:p@127.0.0.1:5432/db?pool_max_conns=notanumber",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parse pool config")
	})

	t.Run("nil workers is rejected by the river client", func(t *testing.T) {
		t.Parallel()
		_, err := riverpkg.NewRuntime(ctx, riverpkg.RuntimeConfig{
			Workers:     nil, // the default queue is always configured, so nil Workers is invalid
			DatabaseURL: unreachableDB,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "new client")
	})

	t.Run("valid config constructs a live runtime", func(t *testing.T) {
		t.Parallel()
		rt, err := riverpkg.NewRuntime(ctx, riverpkg.RuntimeConfig{
			Workers:     river.NewWorkers(),
			DatabaseURL: unreachableDB,
			MaxConns:    4,
			MaxWorkers:  3,
		})
		require.NoError(t, err)
		require.NotNil(t, rt)
		assert.NotNil(
			t,
			rt.Client(),
			"the underlying river client is exposed for transactional enqueue",
		)
		t.Cleanup(func() { _ = rt.Stop(ctx) })
	})
}

// TestRiverRuntime_HealthAndLifecycle tests the runtime's health probes and
// start/stop against an unreachable database.
//
// Why this test is important:
//   - Liveness/Readiness back the worker's health endpoints; Liveness must pass once
//     the pool object exists while Readiness must fail when the database is
//     unreachable, so the two answer different questions (pool constructed vs. server
//     reachable). Start must surface a clear error rather than silently no-op.
//
// What it tests:
//   - Liveness passes once the pool is constructed (offline).
//   - Readiness errors against an unreachable database (the pool ping fails fast).
//   - Start errors when the worker bundle is empty (no worker registered).
//   - Stop closes the pool without error on a never-started runtime.
func TestRiverRuntime_HealthAndLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	rt, err := riverpkg.NewRuntime(ctx, riverpkg.RuntimeConfig{
		Workers:     river.NewWorkers(),
		DatabaseURL: unreachableDB,
	})
	require.NoError(t, err)

	assert.NoError(t, rt.Liveness(ctx), "the pool object is constructed by NewRuntime")

	readyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	require.Error(
		t,
		rt.Readiness(readyCtx),
		"readiness pings the database, which is unreachable",
	)

	startCtx, cancelStart := context.WithTimeout(ctx, 5*time.Second)
	defer cancelStart()
	require.Error(
		t,
		rt.Start(startCtx),
		"an empty worker bundle cannot start working jobs",
	)

	assert.NoError(t, rt.Stop(ctx), "stop drains (nothing in flight) and closes the pool")
}

// TestRiverMigrate_Errors tests that Migrate fails loudly when it cannot reach or
// parse the target database.
//
// Why this test is important:
//   - Migrate applies River's own schema before the worker runtime can start; a
//     swallowed connection or parse failure would let the worker boot against a
//     database missing the river_* tables and crash at first job.
//
// What it tests:
//   - A malformed DSN returns a wrapped open-pool error.
//   - A well-formed but unreachable DSN returns a wrapped apply error (the migrator
//     connects on Migrate, which fails fast).
func TestRiverMigrate_Errors(t *testing.T) {
	t.Parallel()

	t.Run("malformed dsn fails to open the pool", func(t *testing.T) {
		t.Parallel()
		err := riverpkg.Migrate(
			context.Background(),
			"postgres://u:p@127.0.0.1:5432/db?pool_max_conns=notanumber",
		)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "open pool")
	})

	t.Run("unreachable database fails the apply", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := riverpkg.Migrate(ctx, unreachableDB)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "river migrate")
	})
}
