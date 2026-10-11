//go:build integration

// This file verifies the Cassandra analytics store against a real single-node
// Cassandra 5 (testcontainers): bucket fan-out, filter pushdown against an
// in-memory full scan, idempotent redelivery, resumable paging, crash-and-redeliver
// writes, and bucket compaction. The unit suite mocks the session; every behaviour
// that needs real CQL semantics runs here.
package integration

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	analyticscassandra "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/analytics/cassandra"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cassandra"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/chartspec"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
	cassandradb "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/dbtest/cassandra"
	testsuite "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/suite"
)

// itKeyspace is the keyspace the suite bootstraps.
const itKeyspace = "analytics_it"

// itCube is the cube every test writes (isolated per test by org id).
const itCube = "genai_calls"

// AnalyticsCassandraSuite holds a real Cassandra with the cube's tables.
type AnalyticsCassandraSuite struct {
	session cassandra.Session
	testsuite.CassandraIntegrationSuite
}

// TestAnalyticsCassandraSuite runs the Cassandra analytics store integration tests.
//
// Why this test is important:
//   - Without a top-level TestXxx function, `go test` ignores every suite method
//
// What it tests:
//   - Suite entrypoint wires AnalyticsCassandraSuite into the testify runner
func TestAnalyticsCassandraSuite(t *testing.T) {
	suite.Run(t, new(AnalyticsCassandraSuite))
}

// SetupSuite starts the Cassandra container, creates the keyspace and the cube's
// hour and day tables, and opens the session every test shares.
func (s *AnalyticsCassandraSuite) SetupSuite() {
	s.CassandraIntegrationSuite.SetupSuite()
	ctx := context.Background()
	s.Require().NoError(s.Cassandra.Exec(
		ctx,
		"CREATE KEYSPACE IF NOT EXISTS "+itKeyspace+" WITH replication = {'class': "+
			"'SimpleStrategy', 'replication_factor': 1}",
	))
	for _, g := range []types.Grain{types.GrainHour, types.GrainDay} {
		ddl, err := analyticscassandra.SchemaCQL(itKeyspace, itCube, g)
		s.Require().NoError(err)
		s.Require().NoError(s.Cassandra.Exec(ctx, ddl))
	}
	session, err := cassandra.New(cassandra.KindCassandra,
		cassandra.WithHosts(s.CassandraPort, s.CassandraHost),
		cassandra.WithLocalDC(cassandradb.LocalDC),
		cassandra.WithKeyspace(itKeyspace),
		cassandra.WithTimeout(20*time.Second),
	)
	s.Require().NoError(err)
	s.session = session
}

// TearDownSuite closes the shared session, then stops the container.
func (s *AnalyticsCassandraSuite) TearDownSuite() {
	if s.session != nil {
		s.session.Close()
	}
	s.CassandraIntegrationSuite.TearDownSuite()
}

// newStore builds a store over the suite session (pageSize 0 = default).
func (s *AnalyticsCassandraSuite) newStore(
	session cassandra.Session,
	pageSize int,
) *analyticscassandra.Store {
	store, err := analyticscassandra.New(session, analyticscassandra.Config{
		Cubes: map[string][]types.Grain{itCube: {types.GrainHour, types.GrainDay}},
		BucketWidth: map[types.Grain]types.Grain{
			types.GrainHour: types.GrainDay,
			types.GrainDay:  types.GrainMonth,
		},
		PageSize:             pageSize,
		MaxConcurrentBuckets: 4,
	})
	s.Require().NoError(err)
	return store
}

// fact builds a genai_calls fact for org.
func fact(org, key string, at time.Time, team, model string, tokens float64) types.Fact {
	return types.Fact{
		Cube: itCube, OrgID: org, TS: at, IdempotencyKey: key,
		Dims:     map[string]string{"team": team, "model": model},
		Measures: map[string]float64{"tokens_in": tokens},
	}
}

// totals drains q's stream and merges every row's tokens_in partial per group
// (keyed by the group's team value, "" for an ungrouped query).
func (s *AnalyticsCassandraSuite) totals(
	store interfaces.AnalyticsStore,
	q types.AggregateQuery,
) map[string]chartspec.Partial {
	ctx := context.Background()
	stream, err := store.Aggregate(ctx, q)
	s.Require().NoError(err)
	defer func() { s.Require().NoError(stream.Close()) }()
	out := map[string]chartspec.Partial{}
	for {
		rows, more, err := stream.Next(ctx)
		s.Require().NoError(err)
		for _, r := range rows {
			p, err := chartspec.DecodePartial(r.Partials["tokens_in"])
			s.Require().NoError(err)
			acc := out[r.Group["team"]]
			acc.Merge(p)
			out[r.Group["team"]] = acc
		}
		if !more {
			return out
		}
	}
}

// query is the tokens_in-by-team query of org over [from, to) at grain.
func query(org string, grain types.Grain, from, to time.Time) types.AggregateQuery {
	return types.AggregateQuery{
		Cube: itCube, OrgID: org, Grain: grain, GroupBy: []string{"team"},
		Measures:  []types.MeasureRef{{Name: "tokens_in", Agg: types.AggSum}},
		TimeRange: types.TimeRange{From: from, To: to},
	}
}

// TestCassandra_AggregatesAcrossBucketBoundaries tests that facts in adjacent
// partition buckets are all read and merge to the exact total.
//
// Why this test is important:
//   - Hourly rows of one day live in one partition; a range spanning midnight must
//     read both partitions or silently halve the answer
//
// What it tests:
//   - facts at 23:30 and 00:30 (different day buckets) sum to exactly 30 with count 2
func (s *AnalyticsCassandraSuite) TestCassandra_AggregatesAcrossBucketBoundaries() {
	store := s.newStore(s.session, 0)
	org := "org-buckets"
	s.Require().NoError(store.Write(context.Background(), []types.Fact{
		fact(org, "a", time.Date(2026, 10, 8, 23, 30, 0, 0, time.UTC), "t-1", "m", 10),
		fact(org, "b", time.Date(2026, 10, 9, 0, 30, 0, 0, time.UTC), "t-1", "m", 20),
	}))

	got := s.totals(
		store,
		query(
			org,
			types.GrainHour,
			time.Date(2026, 10, 8, 20, 0, 0, 0, time.UTC),
			time.Date(2026, 10, 9, 2, 0, 0, 0, time.UTC),
		),
	)

	s.InDelta(30.0, chartspec.Finalize(got["t-1"], types.AggSum), 0)
	s.InDelta(2.0, chartspec.Finalize(got["t-1"], types.AggCount), 0)
}

// randFilter is one generated filter: its listquery JSON and the same predicate in Go.
type randFilter struct {
	match func(f types.Fact) bool
	json  string
}

// genFilter builds a random filter over team, model and the day-aligned time.
func genFilter(r *rand.Rand, days []time.Time) randFilter {
	teams, models := []string{"t-1", "t-2", "t-3"}, []string{"m-a", "m-b"}
	leaf := func() randFilter {
		switch r.Intn(4) {
		case 0:
			t := teams[r.Intn(len(teams))]
			return randFilter{
				json:  fmt.Sprintf(`{"$eq":{"team":%q}}`, t),
				match: func(f types.Fact) bool { return f.Dims["team"] == t },
			}
		case 1:
			a, b := teams[r.Intn(len(teams))], teams[r.Intn(len(teams))]
			return randFilter{
				json:  fmt.Sprintf(`{"$in":{"team":[%q,%q]}}`, a, b),
				match: func(f types.Fact) bool { return f.Dims["team"] == a || f.Dims["team"] == b },
			}
		case 2:
			m := models[r.Intn(len(models))]
			return randFilter{
				json:  fmt.Sprintf(`{"$ne":{"model":%q}}`, m),
				match: func(f types.Fact) bool { return f.Dims["model"] != m },
			}
		default:
			d := days[r.Intn(len(days))]
			return randFilter{
				json: fmt.Sprintf(`{"$gte":{"ts":%q}}`, d.Format(time.RFC3339)),
				match: func(f types.Fact) bool {
					return !chartspec.Truncate(f.TS, types.GrainDay).Before(d)
				},
			}
		}
	}
	a, b := leaf(), leaf()
	if r.Intn(2) == 0 {
		return randFilter{
			json:  `{"$and":[` + a.json + `,` + b.json + `]}`,
			match: func(f types.Fact) bool { return a.match(f) && b.match(f) },
		}
	}
	return randFilter{
		json:  `{"$or":[` + a.json + `,` + b.json + `]}`,
		match: func(f types.Fact) bool { return a.match(f) || b.match(f) },
	}
}

// TestCassandra_PushdownMatchesFullScan tests, over random filters, that the
// store's answer (time bounds pushed into CQL, the rest applied in Go) equals a
// full in-memory scan of the same facts.
//
// Why this test is important:
//   - Pushdown must only ever cut reads, never rows: any divergence is a wrong number
//
// What it tests:
//   - 80 facts over Sep 25 – Oct 6 (two month buckets); for 30 random $and/$or
//     filters on team, model and ts, the per-team sums equal the full scan's exactly
func (s *AnalyticsCassandraSuite) TestCassandra_PushdownMatchesFullScan() {
	store := s.newStore(s.session, 7)
	org := "org-pushdown"
	r := rand.New(rand.NewSource(42))
	var days []time.Time
	first := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	for d := first; d.Before(end); d = d.AddDate(0, 0, 1) {
		days = append(days, d)
	}
	facts := make([]types.Fact, 0, 80)
	for i := range 80 {
		at := days[r.Intn(len(days))].Add(time.Duration(r.Intn(24*60)) * time.Minute)
		facts = append(facts, fact(
			org,
			fmt.Sprintf("f-%d", i),
			at,
			[]string{"t-1", "t-2", "t-3"}[r.Intn(3)],
			[]string{"m-a", "m-b"}[r.Intn(2)],
			float64(1+r.Intn(100)),
		))
	}
	s.Require().NoError(store.Write(context.Background(), facts))
	m := listquery.NewMap().
		Add(listquery.String("team"), listquery.String("model"), listquery.Time("ts").AsTime())

	for range 30 {
		gen := genFilter(r, days)
		filter, err := listquery.Parse(m, gen.json)
		s.Require().NoError(err, gen.json)
		q := query(org, types.GrainDay, days[0], days[len(days)-1].AddDate(0, 0, 1))
		q.Filter = filter
		got := s.totals(store, q)

		want := map[string]float64{}
		for _, f := range facts {
			if gen.match(f) {
				want[f.Dims["team"]] += f.Measures["tokens_in"]
			}
		}
		gotSums := map[string]float64{}
		for team, p := range got {
			gotSums[team] = chartspec.Finalize(p, types.AggSum)
		}
		s.Equal(want, gotSums, gen.json)
	}
}

// TestCassandra_RedeliveredFactIsNoop tests that writing the same fact twice
// leaves every total unchanged.
//
// Why this test is important:
//   - The fact lane is at-least-once; a redelivery that double-counted would
//     inflate every token and cost panel
//
// What it tests:
//   - one fact written twice sums to its value once (count 1) at both grains
func (s *AnalyticsCassandraSuite) TestCassandra_RedeliveredFactIsNoop() {
	store := s.newStore(s.session, 0)
	org := "org-redeliver"
	f := fact(org, "same", time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC), "t-1", "m", 17)
	s.Require().NoError(store.Write(context.Background(), []types.Fact{f}))
	s.Require().NoError(store.Write(context.Background(), []types.Fact{f}))

	for _, g := range []types.Grain{types.GrainHour, types.GrainDay} {
		got := s.totals(
			store,
			query(
				org,
				g,
				time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC),
				time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC),
			),
		)
		s.InDelta(17.0, chartspec.Finalize(got["t-1"], types.AggSum), 0, g)
		s.InDelta(1.0, chartspec.Finalize(got["t-1"], types.AggCount), 0, g)
	}
}

// TestCassandra_PagingResumeToken tests that a stream resumed from its token
// continues exactly after the last delivered page.
//
// Why this test is important:
//   - A reconnecting browser resumes a query from the last acked frame; a token
//     that skipped or repeated rows would show wrong partial answers
//
// What it tests:
//   - with 2-row pages over 5 rows, the first page has 2 rows and a token; a new
//     stream from that token yields the other 3, with no row twice
func (s *AnalyticsCassandraSuite) TestCassandra_PagingResumeToken() {
	store := s.newStore(s.session, 2)
	org := "org-paging"
	facts := make([]types.Fact, 0, 5)
	for i := range 5 {
		facts = append(
			facts,
			fact(
				org,
				fmt.Sprintf("p-%d", i),
				time.Date(2026, 10, 9, i, 0, 0, 0, time.UTC),
				fmt.Sprintf("t-%d", i),
				"m",
				1,
			),
		)
	}
	s.Require().NoError(store.Write(context.Background(), facts))
	q := query(
		org,
		types.GrainHour,
		time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC),
	)
	ctx := context.Background()

	first, err := store.Aggregate(ctx, q)
	s.Require().NoError(err)
	page, more, err := first.Next(ctx)
	s.Require().NoError(err)
	s.True(more)
	s.Len(page, 2)
	token := first.ResumeToken()
	s.NotEmpty(token)
	s.Require().NoError(first.Close())

	q.ResumeToken = token
	rest := s.totals(store, q)
	seen := map[string]int{}
	for _, r := range page {
		seen[r.Group["team"]]++
	}
	for team := range rest {
		seen[team]++
	}
	s.Equal(map[string]int{"t-0": 1, "t-1": 1, "t-2": 1, "t-3": 1, "t-4": 1}, seen)
}

// crashingSession passes through to a real session but fails the first insert
// into one table — a process dying between a fact's per-grain writes. It is a
// fault-injecting decorator over the real testcontainers session, not a stand-in
// for it: every other statement reaches the real Cassandra, so it is not a test
// double under the policy.
type crashingSession struct {
	cassandra.Session
	table   string
	crashed bool
}

// Query fails the first matching insert's Exec.
func (c *crashingSession) Query(stmt string, values ...any) cassandra.Query {
	q := c.Session.Query(stmt, values...)
	if !c.crashed && strings.HasPrefix(stmt, "INSERT INTO "+c.table+" ") {
		c.crashed = true
		return crashingQuery{Query: q}
	}
	return q
}

// crashingQuery wraps the real query of the failed insert and fails Exec without
// reaching Cassandra; its other methods delegate to the real query.
type crashingQuery struct{ cassandra.Query }

// WithContext keeps the crash armed on the context-bound query.
func (q crashingQuery) WithContext(ctx context.Context) cassandra.Query {
	return crashingQuery{q.Query.WithContext(ctx)}
}

// Idempotent keeps the crash armed on the idempotent query.
func (q crashingQuery) Idempotent(b bool) cassandra.Query {
	return crashingQuery{q.Query.Idempotent(b)}
}

// Exec fails with CodeUnavailable, as a process killed mid-write would.
func (crashingQuery) Exec() error {
	return apperr.New(apperr.CodeUnavailable, "process killed")
}

// TestCassandra_CrashBetweenWritesDoesNotLoseMeasures tests that a write that
// dies after some of a fact's rows landed is repaired exactly by redelivery.
//
// Why this test is important:
//   - A consumer crash after a partial write is redelivered; per-row upserts must
//     neither lose the missing grain nor double the landed one
//
// What it tests:
//   - with the day-table insert failing, Write returns CodeUnavailable; after the
//     redelivered Write, hour and day totals are both exactly 25 with count 1
func (s *AnalyticsCassandraSuite) TestCassandra_CrashBetweenWritesDoesNotLoseMeasures() {
	org := "org-crash"
	f := fact(org, "c-1", time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC), "t-1", "m", 25)
	crashing := s.newStore(
		&crashingSession{
			Session: s.session,
			table:   analyticscassandra.TableName(itCube, types.GrainDay),
		},
		0,
	)

	err := crashing.Write(context.Background(), []types.Fact{f})
	s.Equal(apperr.CodeUnavailable, apperr.Code(err))

	store := s.newStore(s.session, 0)
	s.Require().NoError(store.Write(context.Background(), []types.Fact{f}))
	for _, g := range []types.Grain{types.GrainHour, types.GrainDay} {
		got := s.totals(
			store,
			query(
				org,
				g,
				time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC),
				time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC),
			),
		)
		s.InDelta(25.0, chartspec.Finalize(got["t-1"], types.AggSum), 0, g)
		s.InDelta(1.0, chartspec.Finalize(got["t-1"], types.AggCount), 0, g)
	}
}

// partitionRows counts the stored rows of one partition.
func (s *AnalyticsCassandraSuite) partitionRows(
	org string,
	grain types.Grain,
	bucket time.Time,
) int {
	iter := s.session.Query("SELECT idempotency_key FROM "+analyticscassandra.TableName(
		itCube, grain,
	)+
		" WHERE org_id = ? AND cube = ? AND bucket = ?", org, itCube, bucket).
		Iter()
	n := 0
	var key string
	for iter.Scan(&key) {
		n++
	}
	s.Require().NoError(iter.Close())
	return n
}

// writeCompactionFacts writes 40 facts of one hourly group (t-1) and 3 of another
// (t-2) into the closed day bucket 2025-03-04, returning their tokens per team.
func (s *AnalyticsCassandraSuite) writeCompactionFacts(
	store *analyticscassandra.Store,
	org string,
) map[string][]float64 {
	values := map[string][]float64{}
	facts := make([]types.Fact, 0, 43)
	for i := range 43 {
		team := "t-1"
		if i >= 40 {
			team = "t-2"
		}
		v := float64(1 + i%17)
		values[team] = append(values[team], v)
		facts = append(
			facts,
			fact(
				org,
				fmt.Sprintf("k-%d", i),
				time.Date(2025, 3, 4, 9, i%60, 0, 0, time.UTC),
				team,
				"m",
				v,
			),
		)
	}
	s.Require().NoError(store.Write(context.Background(), facts))
	return values
}

// TestCassandra_CompactionPreservesTotals tests that compacting a closed bucket
// keeps every answer and collapses each group to one row.
//
// Why this test is important:
//   - Compaction exists to bound storage; it must never change a number a panel shows
//
// What it tests:
//   - before and after Compact, sum, count, min, max and p95 per team are identical
//   - the 43 per-fact rows of the hourly partition become 2 compacted rows
func (s *AnalyticsCassandraSuite) TestCassandra_CompactionPreservesTotals() {
	store := s.newStore(s.session, 0)
	org := "org-compact"
	s.writeCompactionFacts(store, org)
	bucket := time.Date(2025, 3, 4, 0, 0, 0, 0, time.UTC)
	q := query(org, types.GrainHour, bucket, bucket.AddDate(0, 0, 1))
	before := s.totals(store, q)
	s.Equal(43, s.partitionRows(org, types.GrainHour, bucket))

	s.Require().
		NoError(store.Compact(context.Background(), itCube, types.GrainHour, org, bucket))

	after := s.totals(store, q)
	s.Equal(2, s.partitionRows(org, types.GrainHour, bucket))
	for _, team := range []string{"t-1", "t-2"} {
		for _, agg := range []types.Aggregate{
			types.AggSum, types.AggCount, types.AggMin, types.AggMax, types.AggP95,
		} {
			s.InDelta(
				chartspec.Finalize(before[team], agg),
				chartspec.Finalize(after[team], agg),
				1e-9,
				"%s %s",
				team,
				agg,
			)
		}
	}
}

// TestCassandra_CompactionIsIdempotent tests that rerunning compaction, and
// compacting again after a late fact, keeps the totals exact.
//
// Why this test is important:
//   - A compaction job can be retried or rerun after a crash; a rerun that merged
//     the compacted row into itself would double every total
//
// What it tests:
//   - two consecutive Compacts give t-1 sum = the exact sum of its 40 values
//   - a late fact (+100) written into the compacted bucket and compacted again
//     raises the sum by exactly 100 and the count by 1, and leaves one row per team
func (s *AnalyticsCassandraSuite) TestCassandra_CompactionIsIdempotent() {
	store := s.newStore(s.session, 0)
	org := "org-compact-twice"
	values := s.writeCompactionFacts(store, org)
	bucket := time.Date(2025, 3, 4, 0, 0, 0, 0, time.UTC)
	q := query(org, types.GrainHour, bucket, bucket.AddDate(0, 0, 1))
	want := 0.0
	for _, v := range values["t-1"] {
		want += v
	}

	ctx := context.Background()
	s.Require().NoError(store.Compact(ctx, itCube, types.GrainHour, org, bucket))
	s.Require().NoError(store.Compact(ctx, itCube, types.GrainHour, org, bucket))
	s.InDelta(want, chartspec.Finalize(s.totals(store, q)["t-1"], types.AggSum), 0)

	lateAt := time.Date(2025, 3, 4, 9, 5, 0, 0, time.UTC)
	late := fact(org, "late", lateAt, "t-1", "m", 100)
	s.Require().NoError(store.Write(ctx, []types.Fact{late}))
	s.Require().NoError(store.Compact(ctx, itCube, types.GrainHour, org, bucket))
	got := s.totals(store, q)["t-1"]
	s.InDelta(want+100, chartspec.Finalize(got, types.AggSum), 0)
	s.InDelta(41.0, chartspec.Finalize(got, types.AggCount), 0)
	s.Equal(2, s.partitionRows(org, types.GrainHour, bucket))
	s.False(math.IsNaN(chartspec.Finalize(got, types.AggP95)))
}
