package unit_test

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/cas"
	casstore "github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/cas"
	caspostgres "github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/cas/postgres"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// casStoreScope is the scope the factory tests address their nodes and refs under.
const casStoreScope = "storetest"

// TestStoresCAS_StubBootsEmptyAndFactoryFailsLoudly tests the cas store factory.
//
// Why this test is important:
//   - A graph that holds a cas store must boot with no database (the stub default),
//     and a misconfigured one (an unknown kind, a postgres kind with no DB, a table
//     name that could carry SQL) must fail at boot, not on the first write.
//
// What it tests:
//   - The default kind is the empty store: Get and Ref are CodeNotFound, Put reports
//     the node stored without keeping it, and Move reports not moved.
//   - An unknown kind, the postgres kind without a DB, and a table name that is not a
//     plain identifier are each CodeInvalidInput.
func TestStoresCAS_StubBootsEmptyAndFactoryFailsLoudly(t *testing.T) {
	t.Parallel()

	stores, err := casstore.NewFromConfig(t.Context(), new(casstore.Config),
		casstore.Deps{DB: nil})
	require.NoError(t, err)

	canonical, err := cas.Canonical([]byte(`{"a":1}`))
	require.NoError(t, err)

	node := types.Node{
		Body: canonical,
		Key: types.NodeKey{
			Scope: casStoreScope,
			Hash:  cas.Sum("storetest/v1", canonical),
		},
	}

	put, err := stores.Nodes.Put(t.Context(), node)
	require.NoError(t, err)
	assert.True(t, put.Stored)

	_, err = stores.Nodes.Get(t.Context(), node.Key)
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeNotFound))

	refKey := types.RefKey{Scope: casStoreScope, Name: "head"}

	_, err = stores.Refs.Ref(t.Context(), refKey)
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeNotFound))

	moved, err := stores.Refs.Move(t.Context(), &types.RefMove{
		Key: refKey, From: types.Hash{}, To: node.Key.Hash,
	})
	require.NoError(t, err)
	assert.False(t, moved)

	assertCASFactoryRefuses(t)
}

// assertCASFactoryRefuses checks each misconfiguration NewFromConfig refuses with
// CodeInvalidInput: an unknown kind, postgres without a DB, an unsafe table name.
func assertCASFactoryRefuses(t *testing.T) {
	t.Helper()

	for name, cfg := range map[string]casstore.Config{
		"unknown kind": fixtures.Build(func(row *casstore.Config) {
			row.Kind = "nosuch"
		}),
		"postgres without a DB": fixtures.Build(func(row *casstore.Config) {
			row.Kind = casstore.KindPostgres
		}),
	} {
		_, err := casstore.NewFromConfig(t.Context(), &cfg, casstore.Deps{DB: nil})
		require.Error(t, err, name)
		assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), name)
	}

	bad := fixtures.Build(func(row *casstore.Config) {
		row.Kind = casstore.KindPostgres
		row.Postgres = caspostgres.Config{
			NodesTable: "nodes; DROP TABLE refs", RefsTable: "",
		}
	})

	_, err := casstore.NewFromConfig(t.Context(), &bad,
		casstore.Deps{DB: new(sql.DB)})
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput))
}
