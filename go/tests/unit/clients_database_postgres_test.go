package unit_test

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // register the pgx database/sql driver
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/database/postgres"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/metrics"
)

// TestStartPoolStatsCollector_PublishesGauges tests that the pool-stats collector
// exposes the DB pool gauges the saturation SLO reads.
//
// Why this test is important:
//   - The DatabaseConnectionPoolSaturation SLO alerts on
//     db_pool_active_connections / db_pool_max_connections; if those series are
//     absent the alert can never fire and pool exhaustion goes unnoticed.
//
// What it tests:
//   - After StartPoolStatsCollector publishes once, /metrics exposes
//     db_pool_max_connections at the configured max plus the active/idle gauges.
func TestStartPoolStatsCollector_PublishesGauges(t *testing.T) {
	t.Parallel()

	// sql.Open does not connect, so Stats() reports the configured caps without a
	// live database — enough to assert the gauges are published.
	db, err := sql.Open("pgx", "postgres://u:p@127.0.0.1:5432/db?sslmode=disable")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(17)

	m, err := metrics.New(metrics.KindPrometheus)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	postgres.StartStatsCollector(
		ctx,
		db,
		m,
		time.Hour,
	) // publishes once synchronously

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/metrics",
		http.NoBody,
	)
	m.Handler().ServeHTTP(rec, req)
	body, err := io.ReadAll(rec.Result().Body)
	require.NoError(t, err)
	out := string(body)

	assert.Contains(t, out, "db_pool_max_connections 17")
	assert.Contains(t, out, "db_pool_active_connections")
	assert.Contains(t, out, "db_pool_idle_connections")
}

// TestCheck_ClosedPoolErrors tests that the readiness closure returned by Check
// reports an error once its pool can no longer reach the database.
//
// Why this test is important:
//   - Check is the readiness probe's DB gate: if the
//     closure did not surface a dead pool, /readyz would stay 200 with a
//     black-holed database — the exact bug this guards against. Pinning that a
//     closed pool makes the closure error is what makes the probe load-bearing.
//
// What it tests:
//   - Check(db)() returns a non-nil error after the pool is closed (a closed pool
//     is the fastest deterministic stand-in for an unreachable database).
func TestCheck_ClosedPoolErrors(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("pgx", "postgres://u:p@127.0.0.1:5432/db?sslmode=disable")
	require.NoError(t, err)

	check := postgres.Check(db)
	require.NoError(
		t,
		db.Close(),
	) // now every ping fails immediately ("database is closed")

	assert.Error(t, check(), "a closed pool must make the readiness check fail")
}

// TestPingDB_BadDSNFailsFast tests that Ping surfaces a clear, wrapped error
// when the database is unreachable, coded as transient.
//
// Why this test is important:
//   - Ping is the boot-time fail-fast and the readiness probe; a bad DSN or down
//     server must produce a clear error at the composition root, not a silent
//     first-request failure.
//   - An unreachable database is transient: coded CodeUnavailable, a caller that
//     classifies by code (IsTransient, the retry policy) retries it instead of
//     treating a DB blip as permanent.
//
// What it tests:
//   - Ping against an unreachable port returns an error naming the ping failure,
//     coded CodeUnavailable.
func TestPingDB_BadDSNFailsFast(t *testing.T) {
	t.Parallel()

	// Port 1 refuses instantly, so the probe fails well within pingTimeout.
	db, err := sql.Open("pgx", "postgres://u:p@127.0.0.1:1/db?sslmode=disable")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	err = postgres.Ping(context.Background(), db)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database ping failed")
	assert.Equal(
		t,
		apperr.CodeUnavailable,
		apperr.Code(err),
		"an unreachable DB is transient",
	)
}
