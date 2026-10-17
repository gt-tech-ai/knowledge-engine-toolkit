package unit_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/sqltx"
)

// TestSQLTx_RefusesANestedTransactionAndANilDB tests the manager's wiring guards.
//
// Why this test is important:
//   - A transaction opened inside another runs on a second connection, so its
//     writes commit even when the outer transaction rolls back.
//
// What it tests:
//   - WithTransaction and Begin on a context already in a transaction fail
//     INTERNAL without touching the (unreachable) database or running the body;
//   - New panics on a nil db.
func TestSQLTx_RefusesANestedTransactionAndANilDB(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("pgx", "postgres://unused")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mgr := sqltx.New(db)
	nested := interfaces.WithInTx(t.Context())

	ran := false
	err = mgr.WithTransaction(nested, func(context.Context) error {
		ran = true
		return nil
	})
	require.Equal(t, apperr.CodeInternal, apperr.Code(err))
	require.False(t, ran)
	_, _, err = mgr.Begin(nested)
	require.Equal(t, apperr.CodeInternal, apperr.Code(err))
	require.Panics(t, func() { sqltx.New(nil) })
}
