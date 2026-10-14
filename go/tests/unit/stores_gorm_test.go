package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
	gormadapter "github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/gorm"
)

// TestGormAdapter_KindAndUnknown tests that the GORM adapter selects the Postgres
// dialect by Kind and rejects an unknown Kind.
//
// Why this test is important:
//   - NewFromConfig is the adapter's app-wiring entrypoint; a silently-accepted
//     unknown Kind would open the wrong (or a nil) dialect, and the Kind string is
//     what logs/metrics report the selected backend as.
//
// What it tests:
//   - KindPostgres is "postgres", and an empty Kind selects it.
//   - NewFromConfig with an unknown Kind returns an INVALID_INPUT error naming it.
func TestGormAdapter_KindAndUnknown(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "postgres", gormadapter.KindPostgres)
	_, err := gormadapter.NewFromConfig(gormadapter.Config{})
	require.NoError(t, err)

	_, err = gormadapter.NewFromConfig(gormadapter.Config{Kind: "mysql"})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.Contains(t, err.Error(), `"mysql"`)
}

// TestGormAdapter_LifecycleUnreachable tests the GORM client's not-started guards and
// its fail-fast Start against an unreachable database, mirroring
// TestPostgresBackend_NotStartedGuards / _StartUnreachable: NewFromConfig does no I/O,
// so before Start the pool is unopened and Start surfaces the unreachable host.
//
// Why this test is important:
//   - A composition root relies on Liveness and Readiness reporting not-ready until
//     Start opens the pool, and on Start failing fast when the database is unreachable
//     rather than deferring the failure to the first query (and NOT doing I/O in the
//     constructor).
//   - A failed Start must not keep the pool: lifecycle.Manager never stops the client
//     whose Start failed, so a retained pool leaks (one per retry) and Liveness would
//     report a client that never connected as live.
//
// What it tests:
//   - The client satisfies interfaces.Client.
//   - NewFromConfig succeeds without dialing (no I/O); before Start, DB() is nil and
//     Liveness/Readiness report not-started, and Stop is a safe no-op.
//   - Start against an unreachable host fails with CodeUnavailable (transient), and
//     afterwards DB() is still nil and Liveness still fails.
func TestGormAdapter_LifecycleUnreachable(t *testing.T) {
	t.Parallel()

	client, err := gormadapter.NewFromConfig(gormadapter.Config{
		Kind: gormadapter.KindPostgres,
		Database: infra.DatabaseConfig{
			Host:     "invalid-host.example.invalid",
			Port:     5432,
			User:     "u",
			Password: "p",
			Database: "d",
			SSLMode:  "disable",
		},
	})
	require.NoError(
		t,
		err,
		"NewFromConfig does no I/O, so an unreachable host is not an error here",
	)
	require.NotNil(t, client)

	var _ interfaces.Client = client
	ctx := context.Background()

	// Before Start: the pool is unopened.
	assert.Nil(t, client.DB(), "the pool is not opened until Start")
	require.Error(t, client.Liveness(ctx), "not started → not live")
	require.Error(t, client.Readiness(ctx), "not started → not ready")
	require.NoError(t, client.Stop(ctx), "Stop before Start is a no-op")

	// Start dials the unreachable host and must fail fast, keeping no pool.
	startErr := client.Start(ctx)
	require.Error(t, startErr, "Start against an unreachable host fails fast")
	assert.Equal(
		t,
		apperr.CodeUnavailable,
		apperr.Code(startErr),
		"an unreachable DB is transient",
	)
	assert.Nil(t, client.DB(), "a failed Start keeps no pool")
	assert.Error(t, client.Liveness(ctx), "a failed Start leaves the client not live")
}
