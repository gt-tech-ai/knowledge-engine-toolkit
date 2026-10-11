//go:build integration

package integration

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx database/sql driver for the assertion query
	"github.com/stretchr/testify/require"

	riverclient "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/jobs/river"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/dbtest/postgres"
)

// TestRiverMigrate_CreatesRiverSchema verifies that river.Migrate creates River's
// own tables against a real Postgres.
//
// Why this test is important:
//   - A River runtime cannot start until River's schema (river_job, river_leader,
//     …) exists. Migrate applies it with River's own migrator, outside the caller's
//     schema-migration tool (whose drift check would otherwise flag the river_*
//     tables); this proves the migrator runs and creates the tables.
//
// What it tests:
//   - river.Migrate(ctx, dsn) creates the river_job and river_leader tables.
func TestRiverMigrate_CreatesRiverSchema(t *testing.T) {
	ctx := context.Background()
	db, err := postgres.NewTestDatabase(ctx)
	require.NoError(t, err, "start test database")
	defer db.Close(ctx)

	require.NoError(t, riverclient.Migrate(ctx, db.GetDSN()), "apply river schema")

	conn, err := sql.Open("pgx", db.GetDSN())
	require.NoError(t, err)
	defer conn.Close()

	for _, table := range []string{"river_job", "river_leader"} {
		var exists bool
		row := conn.QueryRowContext(
			ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`,
			table,
		)
		require.NoError(t, row.Scan(&exists))
		require.Truef(t, exists, "table %q must exist after river.Migrate", table)
	}
}
