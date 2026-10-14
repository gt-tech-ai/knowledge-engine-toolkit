package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/analytics"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cql"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestNewFromConfig_DefaultsToStub tests that the default analytics store is the
// zero-infrastructure stub, and that it behaves as an empty store.
//
// Why this test is important:
//   - The graph must boot in dev and CI with no Cassandra, and rolling back to
//     the stub must leave panels empty rather than erroring
//
// What it tests:
//   - DefaultConfig is KindStub with bucket grains minute→hour, hour→day,
//     day→month, month→month and 8 concurrent buckets
//   - an empty config builds the stub: Start/Stop/Write succeed, Aggregate yields one
//     empty, final page and an empty resume token
func TestNewFromConfig_DefaultsToStub(t *testing.T) {
	t.Parallel()

	cfg := analytics.DefaultConfig()
	assert.Equal(t, analytics.KindStub, cfg.Kind)
	assert.Equal(t, map[types.Grain]types.Grain{
		types.GrainMinute: types.GrainHour, types.GrainHour: types.GrainDay,
		types.GrainDay: types.GrainMonth, types.GrainMonth: types.GrainMonth,
	}, cfg.BucketWidth)
	assert.Equal(t, 8, cfg.MaxConcurrentBuckets)

	ctx := context.Background()
	store, err := analytics.NewFromConfig(ctx, analytics.Config{}, analytics.Deps{})
	require.NoError(t, err)

	require.NoError(t, store.Start(ctx))
	require.NoError(
		t,
		store.Write(ctx, []types.Fact{{Cube: "genai_calls", OrgID: "o", TS: time.Now()}}),
	)
	stream, err := store.Aggregate(
		ctx,
		types.AggregateQuery{Cube: "genai_calls", OrgID: "o"},
	)
	require.NoError(t, err)
	rows, more, err := stream.Next(ctx)
	require.NoError(t, err)
	assert.Empty(t, rows)
	assert.False(t, more)
	assert.Empty(t, stream.ResumeToken())
	require.NoError(t, stream.Close())
	require.NoError(t, store.Stop(ctx))
}

// TestNewFromConfig_CassandraKindDialsNestedConfigAtStart tests that the
// cassandra kind dials its session from the nested CQL config at Start,
// never at construction.
//
// Why this test is important:
//   - Keyspaces-vs-Cassandra connectivity is chosen by the nested CQL.Kind;
//     the analytics factory must pass that config through untouched
//   - Construction does no I/O, so a process whose cluster is unreachable still
//     builds its graph and serves health and readiness
//
// What it tests:
//   - construction does not call the session factory; Start calls it once with
//     exactly cfg.CQL (keyspaces kind, region, keyspace), and a second Start
//     does not dial again; the store is an AnalyticsStore and AnalyticsCompactor
//   - a Write before Start is CodeUnavailable
//   - a session factory error is returned by Start, coded
func TestNewFromConfig_CassandraKindDialsNestedConfigAtStart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	ctrl := gomock.NewController(t)
	session := mocks.NewMockCQLSession(ctrl)
	var got []cql.Config
	cfg := analytics.DefaultConfig()
	cfg.Kind = analytics.KindCassandra
	cfg.CQL.Kind = cql.KindKeyspaces
	cfg.CQL.Keyspaces.Region = "us-east-1"
	cfg.CQL.Keyspace = "analytics"
	cfg.Cubes = map[string][]types.Grain{"genai_calls": {types.GrainHour, types.GrainDay}}
	store, err := analytics.NewFromConfig(ctx, cfg, analytics.Deps{
		Dial: func(_ context.Context, c cql.Config) (interfaces.CQLSession, error) {
			got = append(got, c)
			return session, nil
		},
	})
	require.NoError(t, err)
	assert.Empty(t, got, "no dial at construction")
	assert.Equal(t, apperr.CodeUnavailable, apperr.Code(store.Write(ctx, []types.Fact{
		{Cube: "genai_calls", OrgID: "o", IdempotencyKey: "k"},
	})))

	require.NoError(t, store.Start(ctx))
	require.NoError(t, store.Start(ctx))
	require.Len(t, got, 1)
	assert.Equal(t, cql.KindKeyspaces, got[0].Kind)
	assert.Equal(t, "us-east-1", got[0].Keyspaces.Region)
	assert.Equal(t, "analytics", got[0].Keyspace)
	assert.Implements(t, (*interfaces.AnalyticsStore)(nil), store)
	assert.Implements(t, (*interfaces.AnalyticsCompactor)(nil), store)

	failing, err := analytics.NewFromConfig(
		ctx,
		analytics.Config{Kind: analytics.KindCassandra},
		analytics.Deps{
			Dial: func(context.Context, cql.Config) (interfaces.CQLSession, error) {
				return nil, apperr.New(apperr.CodeUnavailable, "dial refused")
			},
		},
	)
	require.NoError(t, err)
	assert.Equal(t, apperr.CodeUnavailable, apperr.Code(failing.Start(ctx)))
}
