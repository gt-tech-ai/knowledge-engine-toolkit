//go:build integration

package integration

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/sqltx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/dbtest/postgres"
)

// newSQLTxDB starts Postgres with a one-column table and returns the database.
func newSQLTxDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	pg, err := postgres.NewTestDatabase(ctx)
	require.NoError(t, err, "start postgres")
	t.Cleanup(func() { pg.Close(context.Background()) })
	db, err := pg.OpenDB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(ctx, `CREATE TABLE notes (body text NOT NULL)`)
	require.NoError(t, err)
	return db
}

// TestSQLTx_CommitsRollsBackAndRefusesNesting tests the database/sql transaction
// manager against real Postgres.
//
// Why this test is important:
//   - Every raw-SQL store write commits through this manager; a body error that
//     still committed, or a nested transaction that silently opened a second
//     connection, would break the workflow's atomicity.
//
// What it tests:
//   - a successful body's writes, run on the bound *sql.Tx, commit, and its
//     after-commit hook runs once, after the commit;
//   - a failing body's writes roll back, its error keeps its code, and its hook
//     never runs;
//   - a transaction opened on a context already in one fails INTERNAL without
//     running its body, through both WithTransaction and Begin.
func TestSQLTx_CommitsRollsBackAndRefusesNesting(t *testing.T) {
	requireIntegration(t)
	db := newSQLTxDB(t)
	mgr := sqltx.New(db)
	ctx := t.Context()
	insert := func(ctx context.Context, body string) error {
		tx, ok := sqltx.Tx(ctx)
		require.True(t, ok, "the body runs on a bound transaction")
		require.True(t, interfaces.InTx(ctx))
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO notes (body) VALUES ($1)`,
			body,
		); err != nil {
			return apperr.Wrap(err, apperr.CodeInternal, "sqltx test: insert")
		}
		return nil
	}
	count := func() int {
		var n int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM notes`).Scan(&n))
		return n
	}

	hooks := 0
	require.NoError(t, mgr.WithTransaction(ctx, func(ctx context.Context) error {
		interfaces.RegisterAfterCommit(ctx, func() { hooks++ })
		require.Zero(t, hooks, "the hook waits for the commit")
		return insert(ctx, "kept")
	}))
	require.Equal(t, 1, count())
	require.Equal(t, 1, hooks)

	err := mgr.WithTransaction(ctx, func(ctx context.Context) error {
		interfaces.RegisterAfterCommit(ctx, func() { hooks++ })
		require.NoError(t, insert(ctx, "dropped"))
		return apperr.New(apperr.CodeConflict, "body failed")
	})
	require.Equal(t, apperr.CodeConflict, apperr.Code(err))
	require.Equal(t, 1, count(), "the failed body's write rolled back")
	require.Equal(t, 1, hooks, "a rolled-back transaction runs no hook")

	ran := false
	err = mgr.WithTransaction(ctx, func(ctx context.Context) error {
		return mgr.WithTransaction(ctx, func(context.Context) error {
			ran = true
			return nil
		})
	})
	require.Equal(t, apperr.CodeInternal, apperr.Code(err))
	require.False(t, ran, "a nested body never runs")

	txCtx, tx, err := mgr.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, insert(txCtx, "begun"))
	_, _, err = mgr.Begin(txCtx)
	require.Equal(t, apperr.CodeInternal, apperr.Code(err))
	require.NoError(t, tx.Commit())
	require.Equal(t, 2, count())
}
