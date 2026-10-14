package unit_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/analytics"
	analyticscassandra "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/analytics/cassandra"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cql"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// schemaTables returns the tables the schema tests' catalog declares, in creation order.
func schemaTables() []string { return []string{"genai_calls_day", "genai_calls_hour"} }

// newSchemaStore builds a store over session that ensures the genai_calls cube's
// hour and day tables in keyspace "analytics" at Start, polling every millisecond.
func newSchemaStore(
	t *testing.T,
	session *mocks.MockCQLSession,
	awaitActive bool,
	timeout time.Duration,
) *analyticscassandra.Store {
	t.Helper()
	store, err := analyticscassandra.New(session, analyticscassandra.Config{
		Keyspace: "analytics",
		Cubes: map[string][]types.Grain{
			"genai_calls": {types.GrainHour, types.GrainDay},
		},
		BucketWidth: map[types.Grain]types.Grain{
			types.GrainHour: types.GrainDay, types.GrainDay: types.GrainMonth,
		},
		EnsureSchema:       true,
		AwaitActive:        awaitActive,
		SchemaPollInterval: time.Millisecond,
		SchemaTimeout:      timeout,
	})
	require.NoError(t, err)
	return store
}

// expectCreate expects the CREATE TABLE IF NOT EXISTS of table to run once.
func expectCreate(ctrl *gomock.Controller, session *mocks.MockCQLSession, table string) {
	query := mocks.NewMockCQLQuery(ctrl)
	session.EXPECT().
		Query(gomock.Cond(func(stmt any) bool {
			s, _ := stmt.(string)
			return strings.HasPrefix(
				s,
				"CREATE TABLE IF NOT EXISTS analytics."+table+" (",
			)
		})).
		Return(query)
	query.EXPECT().WithContext(gomock.Any()).Return(query)
	query.EXPECT().Exec().Return(nil)
}

// expectStatus expects one read of table's status from system_schema_mcs.tables
// (any number of reads when repeat is set), answering status.
func expectStatus(
	ctrl *gomock.Controller,
	session *mocks.MockCQLSession,
	table, status string,
	repeat bool,
) *gomock.Call {
	query := mocks.NewMockCQLQuery(ctrl)
	iter := mocks.NewMockCQLIter(ctrl)
	call := session.EXPECT().
		Query(gomock.Cond(func(stmt any) bool {
			s, _ := stmt.(string)
			return strings.Contains(s, "FROM system_schema_mcs.tables")
		}), "analytics", table).
		Return(query)
	calls := []*gomock.Call{
		call,
		query.EXPECT().WithContext(gomock.Any()).Return(query),
		query.EXPECT().Idempotent(true).Return(query),
		query.EXPECT().Iter().Return(iter),
		iter.EXPECT().Scan(gomock.Any()).DoAndReturn(func(dest ...any) bool {
			*dest[0].(*string) = status
			return true
		}),
		iter.EXPECT().Close().Return(nil),
	}
	if repeat {
		for _, c := range calls {
			c.AnyTimes()
		}
	}
	return call
}

// TestCassandraStore_EnsureSchemaCreatesEveryDeclaredTable tests that Start creates
// one table per declared cube and grain when EnsureSchema is set.
//
// Why this test is important:
//   - A missing table fails every write and read of its grain; creating them at
//     Start lets a fresh keyspace serve with no manual DDL step
//
// What it tests:
//   - Start runs CREATE TABLE IF NOT EXISTS once per table, in a stable order
//   - with AwaitActive off it reads no table status
//   - a second Start runs no DDL again
func TestCassandraStore_EnsureSchemaCreatesEveryDeclaredTable(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	session := mocks.NewMockCQLSession(ctrl)
	store := newSchemaStore(t, session, false, 0)
	for _, table := range schemaTables() {
		expectCreate(ctrl, session, table)
	}

	require.NoError(t, store.Start(context.Background()))
	require.NoError(t, store.Start(context.Background()))
}

// TestCassandraStore_EnsureSchemaWaitsForKeyspacesActive tests that Start waits for
// each created table to report ACTIVE when AwaitActive is set.
//
// Why this test is important:
//   - Amazon Keyspaces creates tables asynchronously: a write issued before the
//     table is ACTIVE fails, so serving must wait for it
//
// What it tests:
//   - a table answering CREATING is polled again until it answers ACTIVE
//   - Start returns nil once every table is ACTIVE
func TestCassandraStore_EnsureSchemaWaitsForKeyspacesActive(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	session := mocks.NewMockCQLSession(ctrl)
	store := newSchemaStore(t, session, true, time.Minute)
	for _, table := range schemaTables() {
		expectCreate(ctrl, session, table)
	}
	gomock.InOrder(
		expectStatus(ctrl, session, "genai_calls_day", "CREATING", false),
		expectStatus(ctrl, session, "genai_calls_day", "ACTIVE", false),
	)
	expectStatus(ctrl, session, "genai_calls_hour", "ACTIVE", false)

	require.NoError(t, store.Start(context.Background()))
}

// TestNewFromConfig_EnsureSchemaAwaitsActiveOnKeyspaces tests that the factory
// passes EnsureSchema through and waits for ACTIVE only on Amazon Keyspaces.
//
// Why this test is important:
//   - Keyspaces creates tables asynchronously while Apache Cassandra creates them
//     synchronously: the wait must follow the session kind, not a second flag an
//     operator could set inconsistently
//
// What it tests:
//   - with the keyspaces session kind, Start creates each table and reads its status
//   - with the cassandra session kind, Start creates each table and reads no status
//   - with EnsureSchema off, Start runs no DDL
func TestNewFromConfig_EnsureSchemaAwaitsActiveOnKeyspaces(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	build := func(session *mocks.MockCQLSession, kind client.Kind, ensure bool) error {
		cfg := analytics.DefaultConfig()
		cfg.Kind = analytics.KindCassandra
		cfg.CQL.Kind = kind
		cfg.CQL.Keyspace = "analytics"
		cfg.Cubes = map[string][]types.Grain{
			"genai_calls": {types.GrainHour, types.GrainDay},
		}
		cfg.EnsureSchema = ensure
		store, err := analytics.NewFromConfig(ctx, cfg, analytics.Deps{
			Dial: func(context.Context, cql.Config) (interfaces.CQLSession, error) {
				return session, nil
			},
		})
		require.NoError(t, err)
		return store.Start(ctx)
	}

	ctrl := gomock.NewController(t)
	keyspaces := mocks.NewMockCQLSession(ctrl)
	for _, table := range schemaTables() {
		expectCreate(ctrl, keyspaces, table)
		expectStatus(ctrl, keyspaces, table, "ACTIVE", false)
	}
	require.NoError(t, build(keyspaces, cql.KindKeyspaces, true))

	plain := mocks.NewMockCQLSession(ctrl)
	for _, table := range schemaTables() {
		expectCreate(ctrl, plain, table)
	}
	require.NoError(t, build(plain, cql.KindCassandra, true))

	require.NoError(t, build(mocks.NewMockCQLSession(ctrl), cql.KindCassandra, false))
}

// TestCassandraStore_EnsureSchemaTimesOutUnavailable tests that a table that never
// becomes ACTIVE fails Start once the schema timeout passes.
//
// Why this test is important:
//   - An unbounded wait would hang the process at boot instead of failing its
//     readiness, hiding a stuck table from the operator
//
// What it tests:
//   - a table that keeps answering CREATING makes Start return CodeUnavailable
//     within the schema timeout
func TestCassandraStore_EnsureSchemaTimesOutUnavailable(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	session := mocks.NewMockCQLSession(ctrl)
	store := newSchemaStore(t, session, true, 20*time.Millisecond)
	for _, table := range schemaTables() {
		expectCreate(ctrl, session, table)
	}
	expectStatus(ctrl, session, "genai_calls_day", "CREATING", true)

	err := store.Start(context.Background())
	assert.Equal(t, apperr.CodeUnavailable, apperr.Code(err))
}
