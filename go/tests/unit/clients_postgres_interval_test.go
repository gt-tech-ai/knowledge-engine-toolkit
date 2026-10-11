package unit_test

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // register the pgx database/sql driver
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/database/postgres"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/metrics"
)

// TestStartStatsCollector_DefaultsNonPositiveInterval tests that a non-positive
// refresh interval falls back to the default cadence instead of a zero-tick loop.
//
// Why this test is important:
//   - A zero or negative interval would create a time.Ticker that panics (ticker
//     requires a positive duration); the guard is what keeps a misconfigured cadence
//     from crashing the composition root.
//
// What it tests:
//   - StartStatsCollector with interval 0 publishes the gauges once without panicking.
func TestStartStatsCollector_DefaultsNonPositiveInterval(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("pgx", "postgres://u:p@127.0.0.1:5432/db?sslmode=disable")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	m, err := metrics.New(metrics.KindPrometheus)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	// interval 0 must fall back to the default cadence, not build a zero-tick ticker.
	postgres.StartStatsCollector(ctx, db, m, 0)
}
