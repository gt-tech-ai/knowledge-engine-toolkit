package unit_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	analyticscassandra "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/analytics/cassandra"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/chartspec"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// storedRow is one row a mocked Cassandra page returns.
type storedRow struct {
	ts       time.Time
	dims     map[string]string
	partials map[string][]byte
}

// day returns 2026-<month>-<d> 00:00 UTC.
func day(
	month time.Month,
	d int,
) time.Time {
	return time.Date(year2026, month, d, 0, 0, 0, 0, time.UTC)
}

// newCassandraStore builds the store over a mock session with the genai_calls cube
// rolled up hourly and daily.
func newCassandraStore(
	t *testing.T,
	session *mocks.MockCQLSession,
) *analyticscassandra.Store {
	t.Helper()

	store, err := analyticscassandra.New(session, &analyticscassandra.Config{
		Cubes: map[string][]types.Grain{
			textGenaiCalls: {types.GrainHour, types.GrainDay},
		},
		BucketWidth: map[types.Grain]types.Grain{
			types.GrainHour: types.GrainDay, types.GrainDay: types.GrainMonth,
		},
		PageSize:             100,
		MaxConcurrentBuckets: 2,
	})
	require.NoError(t, err)

	return store
}

// expectPage expects one paged SELECT of bucket in [lo, hi) and serves rows as a
// single, final page.
func expectPage(
	tb testing.TB,
	ctrl *gomock.Controller,
	session *mocks.MockCQLSession,
	bucket, lo, hi time.Time,
	rows []storedRow,
) {
	tb.Helper()

	query := mocks.NewMockCQLQuery(ctrl)
	iter := mocks.NewMockCQLIter(ctrl)

	session.EXPECT().
		Query(gomock.Any(), textOrg1, textGenaiCalls, bucket, lo, hi).
		Return(query)
	query.EXPECT().WithContext(gomock.Any()).Return(query)
	query.EXPECT().PageSize(100).Return(query)
	query.EXPECT().PageState(gomock.Nil()).Return(query)
	query.EXPECT().Idempotent(true).Return(query)
	query.EXPECT().Iter().Return(iter)

	i := 0

	iter.EXPECT().
		Scan(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(dest ...any) bool {
			if i >= len(rows) {
				return false
			}

			*asType[*time.Time](tb, dest[0]) = rows[i].ts
			*asType[*map[string]string](tb, dest[1]) = rows[i].dims
			*asType[*map[string][]byte](tb, dest[2]) = rows[i].partials
			i++

			return true
		}).
		Times(len(rows) + 1)
	iter.EXPECT().PageState().Return(nil)
	iter.EXPECT().Close().Return(nil)
}

// encoded returns the encoded single-observation partial of v.
func encoded(t *testing.T, v float64) []byte {
	t.Helper()

	p, err := chartspec.NewPartial(v)
	require.NoError(t, err)

	return chartspec.EncodePartial(p)
}

// TestCassandraStore_ReadsEveryBucketOfTheRangeInOrder tests bucket enumeration:
// a day-grain query reads every monthly bucket its range touches, in order.
//
// Why this test is important:
//   - A missed bucket silently drops a month of data; out-of-order delivery breaks
//     the resume token's position
//
// What it tests:
//   - 2026-09-15 → 2026-11-02 reads buckets Sep 1, Oct 1 and Nov 1 (each once)
//   - pages arrive in bucket order, "more" is true until the last bucket, then the
//     stream ends with no rows
//   - rows are projected to the group-by dimensions and requested partials
func TestCassandraStore_ReadsEveryBucketOfTheRangeInOrder(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	session := mocks.NewMockCQLSession(ctrl)
	store := newCassandraStore(t, session)
	from, to := day(time.September, dayArg15), day(time.November, dayArg2)

	for _, b := range []time.Time{
		day(time.September, 1), day(time.October, 1), day(time.November, 1),
	} {
		expectPage(t, ctrl, session, b, from, to, []storedRow{
			{
				ts:   b.AddDate(0, 0, 1),
				dims: map[string]string{textTeam: b.Month().String(), textModel: "m"},
				partials: map[string][]byte{
					textTokensIn: encoded(t, 1),
					"duration_s": encoded(t, 2),
				},
			},
		})
	}

	stream, err := store.Aggregate(context.Background(), &types.AggregateQuery{
		Cube:      textGenaiCalls,
		OrgID:     textOrg1,
		Grain:     types.GrainDay,
		GroupBy:   []string{textTeam},
		Measures:  []types.MeasureRef{{Name: textTokensIn, Agg: types.AggSum}},
		TimeRange: types.TimeRange{From: from, To: to},
	})
	require.NoError(t, err)

	defer func() { require.NoError(t, stream.Close()) }()

	var (
		teams []string
		mores []bool
	)

	for {
		rows, more, err := stream.Next(context.Background())
		require.NoError(t, err)

		for _, r := range rows {
			teams = append(teams, r.Group[textTeam])
			assert.Equal(t, map[string]string{textTeam: r.Group[textTeam]}, r.Group)
			assert.Len(t, r.Partials, 1)
		}

		mores = append(mores, more)
		if !more {
			break
		}
	}

	assert.Equal(t, []string{"September", "October", "November"}, teams)
	assert.Equal(t, []bool{true, true, false}, mores)
	assert.Empty(t, stream.ResumeToken())
}

// TestCassandraStore_PushdownNarrowsRangeAndFiltersResidual tests the filter
// split: time clauses narrow the CQL clustering range, every clause is applied
// in Go.
//
// Why this test is important:
//   - Pushdown cuts what is read; the residual keeps the answer exact — dimension
//     filters cannot run in CQL without ALLOW FILTERING
//
// What it tests:
//   - ts >= Oct 5 narrows [Oct 1, Nov 1) to [Oct 5, Nov 1) and reads only October
//   - team = t-1 keeps the t-1 row and drops the t-2 row
func TestCassandraStore_PushdownNarrowsRangeAndFiltersResidual(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	session := mocks.NewMockCQLSession(ctrl)
	store := newCassandraStore(t, session)
	m := listquery.NewMap().
		Add(new(listquery.Time("ts")).AsTime(), listquery.String(textTeam))
	filter, err := listquery.Parse(
		m,
		`{"$and":[{"$gte":{"ts":"2026-10-05T00:00:00Z"}},{"$eq":{"team":"t-1"}}]}`,
	)
	require.NoError(t, err)
	expectPage(
		t,
		ctrl,
		session,
		day(time.October, 1),
		day(time.October, dayArg5),
		day(time.November, 1),
		[]storedRow{
			{
				ts:       day(time.October, 6),
				dims:     map[string]string{textTeam: textT1},
				partials: map[string][]byte{textTokensIn: encoded(t, 5)},
			},
			{
				ts:       day(time.October, 7),
				dims:     map[string]string{textTeam: "t-2"},
				partials: map[string][]byte{textTokensIn: encoded(t, 9)},
			},
		},
	)

	stream, err := store.Aggregate(context.Background(), &types.AggregateQuery{
		Cube:      textGenaiCalls,
		OrgID:     textOrg1,
		Grain:     types.GrainDay,
		GroupBy:   []string{textTeam},
		Filter:    filter,
		Measures:  []types.MeasureRef{{Name: textTokensIn, Agg: types.AggSum}},
		TimeRange: types.TimeRange{From: day(time.October, 1), To: day(time.November, 1)},
	})
	require.NoError(t, err)

	rows, more, err := stream.Next(context.Background())
	require.NoError(t, err)
	require.NoError(t, stream.Close())

	assert.False(t, more)
	require.Len(t, rows, 1)
	assert.Equal(t, map[string]string{textTeam: textT1}, rows[0].Group)
	assert.Equal(t, day(time.October, dayArg6), rows[0].TS)
}

// TestCassandraStore_ReadsTheWindowsPartialFirstBucket tests a window that starts
// inside a bucket.
//
// Why this test is important:
//   - A row is stamped at its bucket's start, so filtering rows on ts >= from drops
//     the bucket the window starts in: a "last 24h" read of a day table lost the
//     whole current day, and an hourly trend its first hour.
//
// What it tests:
//   - a day-grain query from Oct 9 02:54 to Oct 10 02:54 reads the Oct 1 partition
//     over [Oct 9 00:00, Oct 10 02:54) and returns the Oct 9 row
func TestCassandraStore_ReadsTheWindowsPartialFirstBucket(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	session := mocks.NewMockCQLSession(ctrl)
	store := newCassandraStore(t, session)
	from := day(time.October, 9).Add(2*time.Hour + 54*time.Minute)
	to := from.Add(24 * time.Hour)
	expectPage(t, ctrl, session, day(time.October, 1), day(time.October, dayArg9), to,
		[]storedRow{{
			ts:       day(time.October, 9),
			dims:     map[string]string{textTeam: textT1},
			partials: map[string][]byte{textTokensIn: encoded(t, 3)},
		}})

	stream, err := store.Aggregate(context.Background(), &types.AggregateQuery{
		Cube:      textGenaiCalls,
		OrgID:     textOrg1,
		Grain:     types.GrainDay,
		GroupBy:   []string{textTeam},
		Measures:  []types.MeasureRef{{Name: textTokensIn, Agg: types.AggSum}},
		TimeRange: types.TimeRange{From: from, To: to},
	})
	require.NoError(t, err)

	rows, more, err := stream.Next(context.Background())
	require.NoError(t, err)
	require.NoError(t, stream.Close())

	assert.False(t, more)
	require.Len(t, rows, 1)
	assert.Equal(t, day(time.October, dayArg9), rows[0].TS)
}

// TestCassandraStore_WriteUpsertsOnePartialRowPerGrain tests the idempotent write
// path: one INSERT per declared grain, keyed by the fact's idempotency key.
//
// Why this test is important:
//   - Without per-grain rows a coarse query would have to scan the finest table;
//     without the idempotency key a redelivered fact would double-count
//
// What it tests:
//   - a fact at 2026-10-09 12:34:56 writes genai_calls_hour (bucket Oct 9, ts 12:00)
//     and genai_calls_day (bucket Oct 1, ts Oct 9), both idempotent, with dims_key
//     "model=m&team=t-1" and a tokens_in partial that finalizes to 42
func TestCassandraStore_WriteUpsertsOnePartialRowPerGrain(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	session := mocks.NewMockCQLSession(ctrl)
	store := newCassandraStore(t, session)
	at := time.Date(year2026, month10, day9, hour12, min34, sec56, 0, time.UTC)
	dims := map[string]string{textTeam: textT1, textModel: "m"}
	insert := "INSERT INTO %s (org_id, cube, bucket, ts, dims_key, idempotency_key, " +
		"dims, partials) VALUES (?, ?, ?, ?, ?, ?, ?, ?)"

	for table, keys := range map[string][iterations2]time.Time{
		"genai_calls_hour": {
			day(time.October, dayArg9), time.Date(year2026, month10, day9, hour12, 0, 0, 0,
				time.UTC),
		},
		textGenaiCallsDay: {day(time.October, 1), day(time.October, dayArg9)},
	} {
		query := mocks.NewMockCQLQuery(ctrl)

		session.EXPECT().
			Query(
				fmt.Sprintf(insert, table),
				textOrg1,
				textGenaiCalls,
				keys[0],
				keys[1],
				"model=m&team=t-1",
				"k-1",
				dims,
				gomock.Any(),
			).
			DoAndReturn(func(_ string, values ...any) *mocks.MockCQLQuery {
				p, err := chartspec.DecodePartial(
					asType[map[string][]byte](t, values[7])[textTokensIn],
				)
				require.NoError(t, err)
				assert.InDelta(t, 42.0, chartspec.Finalize(p, types.AggSum), 0)

				return query
			})
		query.EXPECT().WithContext(gomock.Any()).Return(query)
		query.EXPECT().Idempotent(true).Return(query)
		query.EXPECT().Exec().Return(nil)
	}

	err := store.Write(context.Background(), []types.Fact{
		{
			Cube:           textGenaiCalls,
			OrgID:          textOrg1,
			TS:             at,
			Dims:           dims,
			Measures:       map[string]float64{textTokensIn: 42},
			IdempotencyKey: "k-1",
		},
	})
	require.NoError(t, err)
}

// TestCassandraStore_RejectsInvalidQueriesAndFacts tests the store's coded rejections.
//
// Why this test is important:
//   - An unbounded or org-less read would scan every tenant's partitions; an
//     undeclared cube has no table
//
// What it tests:
//   - Aggregate with an undeclared cube, an undeclared grain, no org, or an open
//     range, and Write with an undeclared cube, no org, an empty idempotency
//     key or an unknown schema version, each return CodeInvalidInput (no
//     session call is made)
//   - New with an unknown grain, or an unknown bucket width, is CodeInvalidInput
func TestCassandraStore_RejectsInvalidQueriesAndFacts(t *testing.T) {
	t.Parallel()

	store := newCassandraStore(t, mocks.NewMockCQLSession(gomock.NewController(t)))
	ctx := context.Background()
	rng := types.TimeRange{From: day(time.October, 1), To: day(time.October, dayArg2)}

	for name, q := range map[string]types.AggregateQuery{
		"cube": {Cube: textOther, OrgID: textO, TimeRange: rng},
		textGrain: {
			Cube: textGenaiCalls, OrgID: textO, Grain: types.GrainMinute,
			TimeRange: rng,
		},
		"org": {Cube: textGenaiCalls, TimeRange: rng},
		"range": {
			Cube: textGenaiCalls, OrgID: textO,
			TimeRange: types.TimeRange{From: rng.From},
		},
	} {
		_, err := store.Aggregate(ctx, &q)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err), name)
	}

	for name, f := range map[string]types.Fact{
		"cube":  {Cube: textOther, OrgID: textO, IdempotencyKey: textK},
		"org":   {Cube: textGenaiCalls, IdempotencyKey: textK},
		textKey: {Cube: textGenaiCalls, OrgID: textO},
		"schema": {
			Cube:           textGenaiCalls,
			OrgID:          textO,
			IdempotencyKey: textK,
			Schema:         types.FactSchemaVersion + 1,
		},
	} {
		assert.Equal(
			t,
			apperr.CodeInvalidInput,
			apperr.Code(store.Write(ctx, []types.Fact{f})),
			name,
		)
	}

	for name, cfg := range map[string]analyticscassandra.Config{
		textGrain: {
			Cubes:       map[string][]types.Grain{textGenaiCalls: {"fortnight"}},
			BucketWidth: map[types.Grain]types.Grain{"fortnight": types.GrainMonth},
		},
		"bucket width": {
			Cubes:       map[string][]types.Grain{textGenaiCalls: {types.GrainDay}},
			BucketWidth: map[types.Grain]types.Grain{types.GrainDay: "quarter"},
		},
	} {
		_, err := analyticscassandra.New(
			mocks.NewMockCQLSession(gomock.NewController(t)),
			&cfg,
		)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err), name)
	}
}

// TestCassandraStore_PageErrorIsTerminal tests that a failed page read ends
// the stream instead of being skipped.
//
// Why this test is important:
//   - Skipping a failed page gives the consumer a shorter result that looks
//     complete, so the aggregates under-count and nothing reports an error
//
// What it tests:
//   - when the first bucket's read fails with an UNAVAILABLE error, Next returns
//     that error with no rows and more=false, and every later Next returns the
//     same error; the second bucket's rows are never delivered
func TestCassandraStore_PageErrorIsTerminal(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	session := mocks.NewMockCQLSession(ctrl)
	store := newCassandraStore(t, session)
	from, to := day(time.September, dayArg15), day(time.October, dayArg15)
	query := mocks.NewMockCQLQuery(ctrl)
	iter := mocks.NewMockCQLIter(ctrl)

	session.EXPECT().
		Query(gomock.Any(), textOrg1, textGenaiCalls, day(time.September, 1), from, to).
		Return(query)
	query.EXPECT().WithContext(gomock.Any()).Return(query)
	query.EXPECT().PageSize(100).Return(query)
	query.EXPECT().PageState(gomock.Nil()).Return(query)
	query.EXPECT().Idempotent(true).Return(query)
	query.EXPECT().Iter().Return(iter)
	iter.EXPECT().Scan(gomock.Any(), gomock.Any(), gomock.Any()).Return(false)
	iter.EXPECT().PageState().Return(nil)
	iter.EXPECT().Close().Return(apperr.New(apperr.CodeUnavailable, "read timed out"))
	expectPage(t, ctrl, session, day(time.October, 1), from, to, []storedRow{{
		ts: day(time.October, 2), dims: map[string]string{textTeam: textT1},
		partials: map[string][]byte{textTokensIn: encoded(t, 1)},
	}})

	stream, err := store.Aggregate(context.Background(), &types.AggregateQuery{
		Cube: textGenaiCalls, OrgID: textOrg1, Grain: types.GrainDay,
		Measures:  []types.MeasureRef{{Name: textTokensIn, Agg: types.AggSum}},
		TimeRange: types.TimeRange{From: from, To: to},
	})
	require.NoError(t, err)

	defer func() { require.NoError(t, stream.Close()) }()

	for range iterations2 {
		rows, more, err := stream.Next(context.Background())
		assert.Equal(t, apperr.CodeUnavailable, apperr.Code(err))
		assert.Nil(t, rows)
		assert.False(t, more)
	}
}

// TestCassandraStore_ResumeTokenIsBoundToItsQuery tests that a resume token only
// resumes the query that issued it.
//
// Why this test is important:
//   - A token from one query applied to another would skip buckets of the new
//     query and return a silently incomplete answer
//
// What it tests:
//   - the token returned after the first of two buckets is rejected as
//     INVALID_INPUT when presented with a different org, with no read issued
func TestCassandraStore_ResumeTokenIsBoundToItsQuery(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	session := mocks.NewMockCQLSession(ctrl)
	store := newCassandraStore(t, session)
	from, to := day(time.September, dayArg15), day(time.October, dayArg15)

	for _, b := range []time.Time{day(time.September, 1), day(time.October, 1)} {
		expectPage(t, ctrl, session, b, from, to, nil)
	}

	q := types.AggregateQuery{
		Cube: textGenaiCalls, OrgID: textOrg1, Grain: types.GrainDay,
		Measures:  []types.MeasureRef{{Name: textTokensIn, Agg: types.AggSum}},
		TimeRange: types.TimeRange{From: from, To: to},
	}
	stream, err := store.Aggregate(context.Background(), &q)
	require.NoError(t, err)

	_, more, err := stream.Next(context.Background())
	require.NoError(t, err)
	require.True(t, more)

	token := stream.ResumeToken()
	require.NoError(t, stream.Close())
	require.NotEmpty(t, token)

	q.OrgID, q.ResumeToken = "org-2", token
	_, err = store.Aggregate(context.Background(), &q)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// newTTLStore builds the store over a mock session with the genai_calls cube
// rolled up hourly (daily buckets) and hourly rows kept for ttl.
func newTTLStore(
	t *testing.T,
	session *mocks.MockCQLSession,
	ttl time.Duration,
) *analyticscassandra.Store {
	t.Helper()

	store, err := analyticscassandra.New(session, &analyticscassandra.Config{
		Cubes:       map[string][]types.Grain{textGenaiCalls: {types.GrainHour}},
		BucketWidth: map[types.Grain]types.Grain{types.GrainHour: types.GrainDay},
		TTL:         map[types.Grain]time.Duration{types.GrainHour: ttl},
		PageSize:    100,
	})
	require.NoError(t, err)

	return store
}

// TestCassandraStore_CompactionKeepsTheBucketsRetention tests that compaction
// writes the merged row with the retention left on its bucket, not a fresh TTL.
//
// Why this test is important:
//   - A fresh TTL on every compaction would extend retention each time a bucket
//     is compacted, keeping data past the configured lifetime
//
// What it tests:
//   - compacting a bucket that closed a day ago, with a 10-day TTL, upserts the
//     merged row USING TTL the seconds left until bucket end + 10 days (at most
//     5 s below the value computed just before the call) and deletes the fact row
//   - compacting a bucket whose bucket end + TTL has passed makes no session
//     call at all and returns nil (its rows expire on their own)
func TestCassandraStore_CompactionKeepsTheBucketsRetention(t *testing.T) {
	t.Parallel()

	ttl := hours10 * hours1024 * time.Hour
	today := time.Now().UTC().Truncate(24 * time.Hour)

	t.Run("within retention", func(t *testing.T) {
		t.Parallel()

		testCassandraStoreCompactionKeepsTheBucketsRetentionCase(t, ttl, today)
	})

	t.Run("past retention", func(t *testing.T) {
		t.Parallel()

		session := mocks.NewMockCQLSession(gomock.NewController(t))
		bucket := today.AddDate(0, 0, -12)

		require.NoError(
			t,
			newTTLStore(
				t,
				session,
				ttl,
			).Compact(context.Background(), textGenaiCalls, types.GrainHour, textOrg1, bucket),
		)
	})
}

// testCassandraStoreCompactionKeepsTheBucketsRetentionCase is the body of a closure in
// TestCassandraStore_CompactionKeepsTheBucketsRetention.
func testCassandraStoreCompactionKeepsTheBucketsRetentionCase(
	t *testing.T,
	ttl time.Duration,
	today time.Time,
) {
	t.Helper()

	ctrl := gomock.NewController(t)
	session := mocks.NewMockCQLSession(ctrl)
	query := mocks.NewMockCQLQuery(ctrl)

	testCassandraStoreCompactionKeepsTheBucketsRetentionRest5(
		t,
		ttl,
		today,
		ctrl,
		session,
		query,
	)
}

// testCassandraStoreCompactionKeepsTheBucketsRetentionRest5 continues
// testCassandraStoreCompactionKeepsTheBucketsRetentionRest3 with its remaining steps.
func testCassandraStoreCompactionKeepsTheBucketsRetentionRest5(
	t *testing.T,
	ttl time.Duration,
	today time.Time,
	ctrl *gomock.Controller,
	session *mocks.MockCQLSession,
	query *mocks.MockCQLQuery,
) {
	t.Helper()

	iter := mocks.NewMockCQLIter(ctrl)
	batch := mocks.NewMockCQLBatch(ctrl)
	bucket := today.AddDate(0, 0, -2)
	ts := bucket.Add(9 * time.Hour)
	dims := map[string]string{textTeam: textT1}

	session.EXPECT().Query(gomock.Any(), textOrg1, textGenaiCalls, bucket).Return(query)
	query.EXPECT().WithContext(gomock.Any()).Return(query)
	query.EXPECT().PageSize(100).Return(query)
	query.EXPECT().PageState(gomock.Nil()).Return(query)
	query.EXPECT().Idempotent(true).Return(query)
	query.EXPECT().Iter().Return(iter)

	served := false

	iter.EXPECT().
		Scan(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(dest ...any) bool {
			if served {
				return false
			}

			served = true
			*asType[*time.Time](t, dest[0]) = ts
			*asType[*string](t, dest[1]) = "team=t-1"
			*asType[*string](t, dest[2]) = "k-1"
			*asType[*map[string]string](t, dest[3]) = dims
			*asType[*map[string][]byte](t, dest[4]) = map[string][]byte{
				textTokensIn: encoded(t, 7),
			}

			return true
		}).
		Times(timesArg2)

	testCassandraStoreCompactionKeepsTheBucketsRetentionRest6(
		t,
		ttl,
		session,
		iter,
		batch,
		bucket,
		ts,
		dims,
	)
}

// testCassandraStoreCompactionKeepsTheBucketsRetentionRest6 continues
// testCassandraStoreCompactionKeepsTheBucketsRetentionRest5 with its remaining steps.
func testCassandraStoreCompactionKeepsTheBucketsRetentionRest6(
	t *testing.T,
	ttl time.Duration,
	session *mocks.MockCQLSession,
	iter *mocks.MockCQLIter,
	batch *mocks.MockCQLBatch,
	bucket, ts time.Time,
	dims map[string]string,
) {
	t.Helper()

	iter.EXPECT().PageState().Return(nil)
	iter.EXPECT().Close().Return(nil)

	want := int(time.Until(bucket.AddDate(0, 0, 1).Add(ttl)) / time.Second)

	session.EXPECT().Batch(gomock.Any()).Return(batch)
	batch.EXPECT().WithContext(gomock.Any()).Return(batch)
	batch.EXPECT().Query(
		"INSERT INTO genai_calls_hour (org_id, cube, bucket, ts, dims_key, "+
			"idempotency_key, dims, partials) VALUES (?, ?, ?, ?, ?, '', ?, ?) USING TTL ?",
		textOrg1, textGenaiCalls, bucket, ts, "team=t-1", dims, gomock.Any(), gomock.Any(),
	).
		Do(func(_ string, values ...any) {
			got := asType[int](t, values[7])
			assert.LessOrEqual(t, got, want)
			assert.GreaterOrEqual(t, got, want-5)
		})

	testCassandraStoreCompactionKeepsTheBucketsRetentionRest(
		t,
		ttl,
		session,
		batch,
		bucket,
		ts,
	)
}

// testCassandraStoreCompactionKeepsTheBucketsRetentionRest continues
// testCassandraStoreCompactionKeepsTheBucketsRetentionCase with its remaining steps.
func testCassandraStoreCompactionKeepsTheBucketsRetentionRest(
	t *testing.T,
	ttl time.Duration,
	session *mocks.MockCQLSession,
	batch *mocks.MockCQLBatch,
	bucket, ts time.Time,
) {
	t.Helper()

	batch.EXPECT().Query(
		"DELETE FROM genai_calls_hour WHERE org_id = ? AND cube = ? AND bucket = ? AND ts "+
			"= ? AND dims_key = ? AND idempotency_key = ?",
		textOrg1, textGenaiCalls, bucket, ts, "team=t-1", "k-1",
	)
	session.EXPECT().ExecuteBatch(batch).Return(nil)

	require.NoError(
		t,
		newTTLStore(
			t,
			session,
			ttl,
		).Compact(context.Background(), textGenaiCalls, types.GrainHour, textOrg1, bucket),
	)
}

// TestCassandraStore_ReadinessQueriesTheCoordinator tests the store's readiness probe.
//
// Why this test is important:
//   - A service gates its /readyz on the store: a probe that always passes keeps a pod
//     whose Cassandra is down in the load balancer, and one that leaks the driver error
//     puts hosts in a probe body.
//
// What it tests:
//   - Readiness runs the system.local query idempotently under the caller's ctx and is
//     nil when it succeeds; a failed query is CodeUnavailable; after Stop the store
//     is CodeUnavailable without touching the session.
func TestCassandraStore_ReadinessQueriesTheCoordinator(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		execErr error
		name    string
	}{
		{name: "ready"},
		{name: "query fails", execErr: apperr.Sentinel("no hosts available")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			session := mocks.NewMockCQLSession(ctrl)
			query := mocks.NewMockCQLQuery(ctrl)
			session.EXPECT().
				Query("SELECT release_version FROM system.local").
				Return(query)
			query.EXPECT().WithContext(gomock.Any()).Return(query)
			query.EXPECT().Idempotent(true).Return(query)
			query.EXPECT().Exec().Return(tc.execErr)

			store := newCassandraStore(t, session)

			err := store.Readiness(t.Context())

			if tc.execErr == nil {
				require.NoError(t, err)

				return
			}

			assert.Equal(t, apperr.CodeUnavailable, apperr.Code(err))
		})
	}

	t.Run("stopped", func(t *testing.T) {
		t.Parallel()

		session := mocks.NewMockCQLSession(gomock.NewController(t))
		session.EXPECT().Close()

		store := newCassandraStore(t, session)
		require.NoError(t, store.Stop(t.Context()))

		err := store.Readiness(t.Context())

		assert.Equal(t, apperr.CodeUnavailable, apperr.Code(err))
	})
}
