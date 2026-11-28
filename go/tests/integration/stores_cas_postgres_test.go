//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	caspg "github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/cas/postgres"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/sqltx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/castest"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/dbtest/postgres"
)

// casSchemaPath is the DDL the postgres kind ships, relative to this package.
const casSchemaPath = "../../stores/cas/postgres/schema.sql"

// newCASStores starts Postgres, applies the shipped schema.sql and returns the stores
// over it with the database they run on.
func newCASStores(t *testing.T) (*caspg.Nodes, *caspg.Refs, *sql.DB) {
	t.Helper()

	ctx := context.Background()

	container, err := postgres.NewTestDatabase(ctx)
	require.NoError(t, err, "start postgres")
	t.Cleanup(func() { container.Close(context.Background()) })

	pool, err := container.OpenDB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })

	schema, err := os.ReadFile(casSchemaPath)
	require.NoError(t, err)

	_, err = pool.ExecContext(ctx, string(schema))
	require.NoError(t, err)

	nodes, refs, err := caspg.New(pool, new(caspg.Config))
	require.NoError(t, err)

	return nodes, refs, pool
}

// TestCASPostgres_NodeAndRefConformance runs the cas conformance suite on Postgres.
//
// Why this test is important:
//   - The stores are the only thing standing between a versioned record and a lost
//     update or a rewritten history; their rules (insert-only nodes, compare-and-swap
//     refs) must hold on the real database, including under concurrent writers, which
//     no mock can show.
//
// What it tests:
//   - the castest NodeStore and RefStore suites pass on the shipped schema.sql.
func TestCASPostgres_NodeAndRefConformance(t *testing.T) {
	t.Parallel()

	nodes, refs, _ := newCASStores(t)

	castest.RunNodeStore(t, context.Background, nodes)
	castest.RunRefStore(t, context.Background, refs)
}

// TestCASPostgres_TransactionAndTableNames tests the postgres kind's transaction
// binding and its table-name check.
//
// Why this test is important:
//   - A caller puts a node and moves its ref in one transaction so a rollback leaves
//     neither behind; and a table name is interpolated into SQL, so one that is not a
//     plain identifier must be refused before any statement is built.
//
// What it tests:
//   - a node put and a ref created inside a rolled-back transaction are gone after it;
//   - a table name carrying SQL is CodeInvalidInput.
func TestCASPostgres_TransactionAndTableNames(t *testing.T) {
	t.Parallel()

	nodes, refs, pool := newCASStores(t)
	ctx := context.Background()

	hash := fixtures.Build(func(row *types.Hash) { row[0] = 1 })
	key := types.NodeKey{Scope: "tx", Hash: hash}
	refKey := types.RefKey{Scope: "tx", Name: "head"}

	rollback := apperr.New(apperr.CodeInternal, "roll back")

	err := sqltx.New(pool).WithTransaction(ctx, func(ctx context.Context) error {
		_, putErr := nodes.Put(ctx, types.Node{Body: []byte(`{"a":1}`), Key: key})
		require.NoError(t, putErr)

		moved, moveErr := refs.Move(ctx, &types.RefMove{
			Key: refKey, From: types.Hash{}, To: hash,
		})
		require.NoError(t, moveErr)
		assert.True(t, moved)

		return rollback
	})
	require.Error(t, err)

	_, err = nodes.Get(ctx, key)
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeNotFound))

	_, err = refs.Ref(ctx, refKey)
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeNotFound))

	_, _, err = caspg.New(pool, &caspg.Config{NodesTable: "a b", RefsTable: ""})
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput))
}
