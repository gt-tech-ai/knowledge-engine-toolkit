//go:build integration

// This file verifies the Cassandra analytics store against a real single-node
// Cassandra 5 (testcontainers): bucket fan-out, filter pushdown against an
// in-memory full scan, idempotent redelivery, resumable paging, crash-and-redeliver
// writes, and bucket compaction. The unit suite mocks the session; every behaviour
// that needs real CQL semantics runs here.

package integration_test

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	analyticscassandra "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/analytics/cassandra"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cql"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/chartspec"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	cassandradb "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/dbtest/cassandra"
	testsuite "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/suite"
)

// itKeyspace is the keyspace the suite bootstraps.
const itKeyspace = "analytics_it"

// itCube is the cube every test writes (isolated per test by org id).
const itCube = "genai_calls"

// AnalyticsCassandraSuite holds a real Cassandra with the cube's tables.
type AnalyticsCassandraSuite struct {
	*testsuite.CassandraIntegrationSuite

	session interfaces.CQLSession
}

// TestAnalyticsCassandraSuite runs the Cassandra analytics store integration tests.
//
// Why this test is important:
//   - Without a top-level TestXxx function, `go test` ignores every suite method
//
// What it tests:
//   - Suite entrypoint wires AnalyticsCassandraSuite into the testify runner
func TestAnalyticsCassandraSuite(t *testing.T) {
	t.Parallel()

	suite.Run(
		t,
		&AnalyticsCassandraSuite{
			CassandraIntegrationSuite: new(testsuite.CassandraIntegrationSuite),
			session:                   nil,
		},
	)
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

	cfg := cql.DefaultConfig()

	cfg.Cassandra.Hosts, cfg.Cassandra.Port = []string{s.CassandraHost}, s.CassandraPort
	cfg.Cassandra.LocalDC = cassandradb.LocalDC
	cfg.Keyspace, cfg.Timeout = itKeyspace, seconds*time.Second

	session, err := cql.NewFromConfig(ctx, &cfg, cql.Deps{})
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
		fact(org, "a", time.Date(2026, 10, 8, 23, 30, 0, 0, time.UTC), textT1, textM, 10),
		fact(org, "b", time.Date(2026, 10, 9, 0, 30, 0, 0, time.UTC), textT1, textM, 20),
	}))

	got := s.totals(
		store,
		new(query(
			org,
			types.GrainHour,
			time.Date(
				dateInput,
				dateInputBoundaries,
				dateInputBucketBoundaries,
				dateInputAcrossBucketBoundaries,
				0,
				0,
				0,
				time.UTC,
			),
			time.Date(
				dateInput,
				dateInputBoundaries,
				dateInputAggregatesAcrossBucketBoundaries,
				dateInputCassandraAggregatesAcrossBucketBoundaries,
				0,
				0,
				0,
				time.UTC,
			),
		)),
	)

	s.InDelta(wantResult, chartspec.Finalize(got[textT1], types.AggSum), 0)
	s.InDelta(wantResultBoundaries, chartspec.Finalize(got[textT1], types.AggCount), 0)
}

// fact builds a genai_calls fact for org.
func fact(org, key string, at time.Time, team, model string, tokens float64) types.Fact {
	return types.Fact{
		Cube: itCube, OrgID: org, TS: at, IdempotencyKey: key,
		Dims:     map[string]string{textTeam: team, textModel: model},
		Measures: map[string]float64{textTokensIn: tokens},
		Schema:   0,
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
	store := s.newStore(s.session, newStoreInput)
	org := "org-pushdown"
	sequence := fixtures.NewSequence(newSequenceInput)

	var days []time.Time

	first := time.Date(dateInput, dateInputScan, dateInputFullScan, 0, 0, 0, 0, time.UTC)
	end := time.Date(
		dateInput,
		dateInputMatchesFullScan,
		dateInputPushdownMatchesFullScan,
		0,
		0,
		0,
		0,
		time.UTC,
	)

	for d := first; d.Before(end); d = d.AddDate(0, 0, 1) {
		days = append(days, d)
	}

	facts := make([]types.Fact, 0, makeInput)

	for index := range iterations {
		instant := days[sequence.IntN(len(days))].Add(
			time.Duration(sequence.IntN(24*60)) * time.Minute,
		)

		facts = append(facts, fact(
			org,
			fmt.Sprintf("f-%d", index),
			instant,
			[]string{textT1, textT2, textT3}[sequence.IntN(3)],
			[]string{"m-a", "m-b"}[sequence.IntN(2)],
			float64(1+sequence.IntN(100)),
		))
	}

	s.Require().NoError(store.Write(context.Background(), facts))

	testCassandraPushdownMatchesFullScanBuildsFilterMap(
		s,
		store,
		org,
		sequence,
		days,
		facts,
	)
}

// testCassandraPushdownMatchesFullScanBuildsFilterMap continues
// TestCassandra_PushdownMatchesFullScan: builds filter map.
func testCassandraPushdownMatchesFullScanBuildsFilterMap(
	analyticsCassandraSuite *AnalyticsCassandraSuite,
	store *analyticscassandra.Store,
	org string,
	sequence *fixtures.Sequence,
	days []time.Time,
	facts []types.Fact,
) {
	fieldMap := listquery.NewMap().
		Add(listquery.String(textTeam), listquery.String(textModel),
			new(listquery.Time("ts")).AsTime())

	testCassandraPushdownMatchesFullScanComparesRandomFilters(
		analyticsCassandraSuite,
		store,
		org,
		sequence,
		days,
		facts,
		fieldMap,
	)
}

// testCassandraPushdownMatchesFullScanComparesRandomFilters continues
// testCassandraPushdownMatchesFullScanBuildsFilterMap: compares random filters.
func testCassandraPushdownMatchesFullScanComparesRandomFilters(
	analyticsCassandraSuite *AnalyticsCassandraSuite,
	store *analyticscassandra.Store,
	org string,
	sequence *fixtures.Sequence,
	days []time.Time,
	facts []types.Fact,
	m *listquery.Map,
) {
	for range iterationsFilters {
		gen := genFilter(sequence, days)
		filter, err := listquery.Parse(m, gen.json)
		analyticsCassandraSuite.Require().NoError(err, gen.json)

		q := query(org, types.GrainDay, days[0], days[len(days)-1].AddDate(0, 0, 1))

		q.Filter = filter

		got := analyticsCassandraSuite.totals(store, &q)

		want := matchingTotals(facts, gen)
		gotSums := finalizedSums(got)

		analyticsCassandraSuite.Equal(want, gotSums, gen.json)
	}
}

// query is the tokens_in-by-team query of org over [from, to) at grain.
func query(
	org string,
	grain types.Grain,
	from, timeValue time.Time,
) types.AggregateQuery {
	return fixtures.Build(func(row *types.AggregateQuery) {
		row.Cube = itCube
		row.OrgID = org
		row.Grain = grain
		row.GroupBy = []string{textTeam}
		row.Measures = []types.MeasureRef{{Name: textTokensIn, Agg: types.AggSum}}
		row.TimeRange = types.TimeRange{From: from, To: timeValue}
	})
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
	currentFact := fact(
		org,
		"same",
		time.Date(
			dateInput,
			dateInputNoop,
			dateInputIsNoop,
			dateInputNoop,
			0,
			0,
			0,
			time.UTC,
		),
		textT1,
		textM,
		factInput,
	)
	s.Require().NoError(store.Write(context.Background(), []types.Fact{currentFact}))
	s.Require().NoError(store.Write(context.Background(), []types.Fact{currentFact}))

	for _, grain := range []types.Grain{types.GrainHour, types.GrainDay} {
		got := s.totals(
			store,
			new(query(
				org,
				grain,
				time.Date(
					dateInput,
					dateInputNoop,
					dateInputIsNoop,
					0,
					0,
					0,
					0,
					time.UTC,
				),
				time.Date(dateInput, dateInputNoop, dateInputNoop, 0, 0, 0, 0, time.UTC),
			)),
		)
		s.InDelta(wantResultNoop, chartspec.Finalize(got[textT1], types.AggSum), 0, grain)
		s.InDelta(
			wantResultIsNoop,
			chartspec.Finalize(got[textT1], types.AggCount),
			0,
			grain,
		)
	}
}

// randFilter is one generated filter: its listquery JSON and the same predicate in Go.
type randFilter struct {
	match func(f types.Fact) bool
	json  string
}

// genFilter builds a random filter over team, model and the day-aligned time.
func genFilter(sequence *fixtures.Sequence, days []time.Time) randFilter {
	teams, models := []string{textT1, textT2, textT3}, []string{"m-a", "m-b"}

	leaf := func() randFilter { return genLeafFilter(sequence, days, teams, models) }

	secondRandFilter, currentRandFilter := leaf(), leaf()

	if sequence.IntN(upperBound) == 0 {
		return randFilter{
			json: `{"$and":[` + secondRandFilter.json + `,` + currentRandFilter.json + `]}`,
			match: func(f types.Fact) bool {
				return secondRandFilter.match(f) &&
					currentRandFilter.match(f)
			},
		}
	}

	return randFilter{
		json: `{"$or":[` + secondRandFilter.json + `,` + currentRandFilter.json + `]}`,
		match: func(f types.Fact) bool {
			return secondRandFilter.match(f) ||
				currentRandFilter.match(f)
		},
	}
}

// genLeafFilter draws one single-condition filter with its in-memory equivalent.
func genLeafFilter(
	sequence *fixtures.Sequence,
	days []time.Time,
	teams, models []string,
) randFilter {
	switch sequence.IntN(upperBoundFilter) {
	case 0:
		t := teams[sequence.IntN(len(teams))]

		return randFilter{
			json:  fmt.Sprintf(`{"$eq":{"team":%q}}`, t),
			match: func(f types.Fact) bool { return f.Dims[textTeam] == t },
		}
	case 1:
		a, b := teams[sequence.IntN(len(teams))], teams[sequence.IntN(len(teams))]

		return randFilter{
			json: fmt.Sprintf(`{"$in":{"team":[%q,%q]}}`, a, b),
			match: func(f types.Fact) bool {
				return f.Dims[textTeam] == a || f.Dims[textTeam] == b
			},
		}
	case intN:
		m := models[sequence.IntN(len(models))]

		return randFilter{
			json:  fmt.Sprintf(`{"$ne":{"model":%q}}`, m),
			match: func(f types.Fact) bool { return f.Dims[textModel] != m },
		}
	default:
		d := days[sequence.IntN(len(days))]

		return randFilter{
			json: fmt.Sprintf(`{"$gte":{"ts":%q}}`, d.Format(time.RFC3339)),
			match: func(f types.Fact) bool {
				return !chartspec.Truncate(f.TS, types.GrainDay).Before(d)
			},
		}
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
	store := s.newStore(s.session, newStoreInputToken)
	org := "org-paging"
	facts := make([]types.Fact, 0, makeInputToken)

	for index := range iterationsToken {
		facts = append(
			facts,
			fact(
				org,
				fmt.Sprintf("p-%d", index),
				time.Date(
					dateInput,
					dateInputToken,
					dateInputResumeToken,
					index,
					0,
					0,
					0,
					time.UTC,
				),
				fmt.Sprintf("t-%d", index),
				textM,
				1,
			),
		)
	}

	s.Require().NoError(store.Write(context.Background(), facts))

	testCassandraPagingResumeTokenResumes(s, store, org)
}

// testCassandraPagingResumeTokenResumes continues TestCassandra_PagingResumeToken.
func testCassandraPagingResumeTokenResumes(
	analyticsCassandraSuite *AnalyticsCassandraSuite,
	store *analyticscassandra.Store,
	org string,
) {
	aggregateQuery := query(
		org,
		types.GrainHour,
		time.Date(dateInput, dateInputToken, dateInputResumeToken, 0, 0, 0, 0, time.UTC),
		time.Date(dateInput, dateInputToken, dateInputToken, 0, 0, 0, 0, time.UTC),
	)
	ctx := context.Background()

	first, err := store.Aggregate(ctx, &aggregateQuery)
	analyticsCassandraSuite.Require().NoError(err)

	page, more, err := first.Next(ctx)
	analyticsCassandraSuite.Require().NoError(err)
	analyticsCassandraSuite.True(more)
	analyticsCassandraSuite.Len(page, lenInput)

	token := first.ResumeToken()
	analyticsCassandraSuite.NotEmpty(token)
	analyticsCassandraSuite.Require().NoError(first.Close())

	aggregateQuery.ResumeToken = token

	rest := analyticsCassandraSuite.totals(store, &aggregateQuery)
	seen := map[string]int{}

	for _, r := range page {
		seen[r.Group[textTeam]]++
	}

	for team := range rest {
		seen[team]++
	}

	analyticsCassandraSuite.Equal(
		map[string]int{"t-0": 1, textT1: 1, textT2: 1, textT3: 1, "t-4": 1},
		seen,
	)
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
	currentFact := fact(
		org,
		"c-1",
		time.Date(
			dateInput,
			dateInputMeasures,
			dateInputLoseMeasures,
			dateInputNotLoseMeasures,
			0,
			0,
			0,
			time.UTC,
		),
		textT1,
		textM,
		factInputMeasures,
	)
	crashing := s.newStore(
		&crashingSession{
			CQLSession: s.session,
			table:      analyticscassandra.TableName(itCube, types.GrainDay),
			crashed:    false,
		},
		0,
	)

	err := crashing.Write(context.Background(), []types.Fact{currentFact})
	s.Equal(apperr.CodeUnavailable, apperr.Code(err))

	store := s.newStore(s.session, 0)
	s.Require().NoError(store.Write(context.Background(), []types.Fact{currentFact}))

	testCassandraCrashBetweenWritesChecksMeasures(s, org, store)
}

// testCassandraCrashBetweenWritesChecksMeasures continues
// TestCassandra_CrashBetweenWritesDoesNotLoseMeasures.
func testCassandraCrashBetweenWritesChecksMeasures(
	analyticsCassandraSuite *AnalyticsCassandraSuite,
	org string,
	store *analyticscassandra.Store,
) {
	for _, grain := range []types.Grain{types.GrainHour, types.GrainDay} {
		got := analyticsCassandraSuite.totals(
			store,
			new(query(
				org,
				grain,
				time.Date(
					dateInput,
					dateInputMeasures,
					dateInputLoseMeasures,
					0,
					0,
					0,
					0,
					time.UTC,
				),
				time.Date(
					dateInput,
					dateInputMeasures,
					dateInputMeasures,
					0,
					0,
					0,
					0,
					time.UTC,
				),
			)),
		)
		analyticsCassandraSuite.InDelta(
			wantResultMeasures,
			chartspec.Finalize(got[textT1], types.AggSum),
			0,
			grain,
		)
		analyticsCassandraSuite.InDelta(
			wantResultLoseMeasures,
			chartspec.Finalize(got[textT1], types.AggCount),
			0,
			grain,
		)
	}
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

	bucket := time.Date(
		dateInputTotals,
		dateInputPreservesTotals,
		dateInputCompactionPreservesTotals,
		0,
		0,
		0,
		0,
		time.UTC,
	)
	aggregateQuery := query(org, types.GrainHour, bucket, bucket.AddDate(0, 0, 1))
	before := s.totals(store, &aggregateQuery)
	s.Equal(equalInput, s.partitionRows(org, types.GrainHour, bucket))

	s.Require().
		NoError(store.Compact(context.Background(), itCube, types.GrainHour, org, bucket))

	after := s.totals(store, &aggregateQuery)
	s.Equal(equalInputTotals, s.partitionRows(org, types.GrainHour, bucket))

	for _, team := range []string{textT1, textT2} {
		for _, agg := range []types.Aggregate{
			types.AggSum, types.AggCount, types.AggMin, types.AggMax, types.AggP95,
		} {
			s.InDelta(
				chartspec.Finalize(before[team], agg),
				chartspec.Finalize(after[team], agg),
				floatTolerance,
				"%s %s",
				team,
				agg,
			)
		}
	}
}

// crashingSession passes through to a real session but fails the first insert
// into one table — a process dying between a fact's per-grain writes. It is a
// fault-injecting decorator over the real testcontainers session, not a stand-in
// for it: every other statement reaches the real Cassandra, so it is not a test
// double under the policy.
type crashingSession struct {
	interfaces.CQLSession

	table   string
	crashed bool
}

// Query fails the first matching insert's Exec.
func (c *crashingSession) Query(stmt string, values ...any) interfaces.CQLQuery {
	cqlQuery := c.CQLSession.Query(stmt, values...)
	if !c.crashed && strings.HasPrefix(stmt, "INSERT INTO "+c.table+" ") {
		c.crashed = true

		return crashingQuery{CQLQuery: cqlQuery}
	}

	return cqlQuery
}

// crashingQuery wraps the real query of the failed insert and fails Exec without
// reaching Cassandra; its other methods delegate to the real query.
type crashingQuery struct{ interfaces.CQLQuery }

// WithContext keeps the crash armed on the context-bound query.
func (q crashingQuery) WithContext(ctx context.Context) interfaces.CQLQuery {
	return crashingQuery{q.CQLQuery.WithContext(ctx)}
}

// Idempotent keeps the crash armed on the idempotent query.
func (q crashingQuery) Idempotent(b bool) interfaces.CQLQuery {
	return crashingQuery{q.CQLQuery.Idempotent(b)}
}

// Exec fails with CodeUnavailable, as a process killed mid-write would.
func (crashingQuery) Exec() error {
	return apperr.New(apperr.CodeUnavailable, "process killed")
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
	bucket := time.Date(
		dateInputIdempotent,
		dateInputIsIdempotent,
		dateInputCompactionIsIdempotent,
		0,
		0,
		0,
		0,
		time.UTC,
	)
	aggregateQuery := query(org, types.GrainHour, bucket, bucket.AddDate(0, 0, 1))
	want := initialWant

	for _, v := range values[textT1] {
		want += v
	}

	ctx := context.Background()
	s.Require().NoError(store.Compact(ctx, itCube, types.GrainHour, org, bucket))
	s.Require().NoError(store.Compact(ctx, itCube, types.GrainHour, org, bucket))
	s.InDelta(
		want,
		chartspec.Finalize(s.totals(store, &aggregateQuery)[textT1], types.AggSum),
		0,
	)

	lateAt := time.Date(
		dateInputIdempotent,
		dateInputIsIdempotent,
		dateInputCompactionIsIdempotent,
		dateInputCassandraCompactionIsIdempotent,
		dateInputTestCassandraCompactionIsIdempotent,
		0,
		0,
		time.UTC,
	)
	late := fact(org, "late", lateAt, textT1, textM, factInputIdempotent)
	s.Require().NoError(store.Write(ctx, []types.Fact{late}))
	s.Require().NoError(store.Compact(ctx, itCube, types.GrainHour, org, bucket))

	got := s.totals(store, &aggregateQuery)[textT1]
	s.InDelta(want+100, chartspec.Finalize(got, types.AggSum), 0)
	s.InDelta(wantResultIdempotent, chartspec.Finalize(got, types.AggCount), 0)
	s.Equal(equalInputIdempotent, s.partitionRows(org, types.GrainHour, bucket))
	s.False(math.IsNaN(chartspec.Finalize(got, types.AggP95)))
}

// newStore builds a store over the suite session (pageSize 0 = default).
func (s *AnalyticsCassandraSuite) newStore(
	session interfaces.CQLSession,
	pageSize int,
) *analyticscassandra.Store {
	s.T().Helper()

	store, err := analyticscassandra.New(
		session,
		fixtures.BuildRef(func(row *analyticscassandra.Config) {
			row.Cubes = map[string][]types.Grain{
				itCube: {types.GrainHour, types.GrainDay},
			}
			row.BucketWidth = map[types.Grain]types.Grain{
				types.GrainHour: types.GrainDay,
				types.GrainDay:  types.GrainMonth,
			}
			row.PageSize = pageSize
			row.MaxConcurrentBuckets = 4
		}),
	)
	s.Require().NoError(err)

	return store
}

// totals drains q's stream and merges every row's tokens_in partial per group
// (keyed by the group's team value, "" for an ungrouped query).
func (s *AnalyticsCassandraSuite) totals(
	store interfaces.AnalyticsStore,
	q *types.AggregateQuery,
) map[string]chartspec.Partial {
	s.T().Helper()

	ctx := context.Background()
	stream, err := store.Aggregate(ctx, q)
	s.Require().NoError(err)

	defer func() { s.Require().NoError(stream.Close()) }()

	out := map[string]chartspec.Partial{}

	for {
		rows, more, err := stream.Next(ctx)
		s.Require().NoError(err)

		for _, row := range rows {
			p, err := chartspec.DecodePartial(row.Partials[textTokensIn])
			s.Require().NoError(err)

			acc := out[row.Group[textTeam]]
			acc.Merge(p)

			out[row.Group[textTeam]] = acc
		}

		if !more {
			return out
		}
	}
}

// partitionRows counts the stored rows of one partition.
func (s *AnalyticsCassandraSuite) partitionRows(
	org string,
	grain types.Grain,
	bucket time.Time,
) int {
	s.T().Helper()

	iter := s.session.Query("SELECT idempotency_key FROM "+analyticscassandra.TableName(
		itCube, grain,
	)+
		" WHERE org_id = ? AND cube = ? AND bucket = ?", org, itCube, bucket).
		Iter()
	count := 0

	var key string

	for iter.Scan(&key) {
		count++
	}

	s.Require().NoError(iter.Close())

	return count
}

// writeCompactionFacts writes 40 facts of one hourly group (t-1) and 3 of another
// (t-2) into the closed day bucket 2025-03-04, returning their tokens per team.
func (s *AnalyticsCassandraSuite) writeCompactionFacts(
	store *analyticscassandra.Store,
	org string,
) map[string][]float64 {
	s.T().Helper()

	values := map[string][]float64{}
	facts := make([]types.Fact, 0, makeInputFacts)

	for index := range iterationsFacts {
		team := textT1
		if index >= wantIndex {
			team = textT2
		}

		amount := float64(1 + index%17)

		values[team] = append(values[team], amount)
		facts = append(
			facts,
			fact(
				org,
				fmt.Sprintf("k-%d", index),
				time.Date(
					dateInputFacts,
					dateInputCompactionFacts,
					dateInputWriteCompactionFacts,
					compactionFactsHour,
					index%60,
					0,
					0,
					time.UTC,
				),
				team,
				textM,
				amount,
			),
		)
	}

	s.Require().NoError(store.Write(context.Background(), facts))

	return values
}

// matchingTotals sums tokens_in per team over the facts the filter matches.
func matchingTotals(facts []types.Fact, gen randFilter) map[string]float64 {
	want := map[string]float64{}

	for _, f := range facts {
		if gen.match(f) {
			want[f.Dims[textTeam]] += f.Measures[textTokensIn]
		}
	}

	return want
}

// finalizedSums reads each team's partial as its sum.
func finalizedSums(got map[string]chartspec.Partial) map[string]float64 {
	sums := map[string]float64{}

	for team, p := range got {
		sums[team] = chartspec.Finalize(p, types.AggSum)
	}

	return sums
}

const (
	// dateInputAcrossBucketBoundaries is the date input across bucket boundaries the tests
	// use.
	dateInputAcrossBucketBoundaries = 20

	// dateInputAggregatesAcrossBucketBoundaries is the date input aggregates across bucket
	// boundaries the tests use.
	dateInputAggregatesAcrossBucketBoundaries = 9

	// dateInputBoundaries is the date input boundaries the tests use.
	dateInputBoundaries = 10

	// dateInputBucketBoundaries is the date input bucket boundaries the tests use.
	dateInputBucketBoundaries = 8

	// dateInputCassandraAggregatesAcrossBucketBoundaries is the date input cassandra
	// aggregates across bucket boundaries the tests use.
	dateInputCassandraAggregatesAcrossBucketBoundaries = 2

	// dateInputCassandraCompactionIsIdempotent is the date input cassandra compaction is
	// idempotent the tests use.
	dateInputCassandraCompactionIsIdempotent = 9

	// dateInputCompactionFacts is the date input compaction facts the tests use.
	dateInputCompactionFacts = 3

	// dateInputCompactionIsIdempotent is the date input compaction is idempotent the tests
	// use.
	dateInputCompactionIsIdempotent = 4

	// dateInputCompactionPreservesTotals is the date input compaction preserves totals the
	// tests use.
	dateInputCompactionPreservesTotals = 4

	// dateInputFacts is the date input facts the tests use.
	dateInputFacts = 2025

	// dateInputFullScan is the date input full scan the tests use.
	dateInputFullScan = 25

	// dateInputIdempotent is the date input idempotent the tests use.
	dateInputIdempotent = 2025

	// dateInputIsIdempotent is the date input is idempotent the tests use.
	dateInputIsIdempotent = 3

	// dateInputIsNoop is the date input is noop the tests use.
	dateInputIsNoop = 9

	// dateInputLoseMeasures is the date input lose measures the tests use.
	dateInputLoseMeasures = 9

	// dateInputMatchesFullScan is the date input matches full scan the tests use.
	dateInputMatchesFullScan = 10

	// dateInputMeasures is the date input measures the tests use.
	dateInputMeasures = 10

	// dateInputNoop is the date input noop the tests use.
	dateInputNoop = 10

	// dateInputNotLoseMeasures is the date input not lose measures the tests use.
	dateInputNotLoseMeasures = 14

	// dateInputPreservesTotals is the date input preserves totals the tests use.
	dateInputPreservesTotals = 3

	// dateInputPushdownMatchesFullScan is the date input pushdown matches full scan the
	// tests use.
	dateInputPushdownMatchesFullScan = 7

	// dateInputResumeToken is the date input resume token the tests use.
	dateInputResumeToken = 9

	// dateInputScan is the date input scan the tests use.
	dateInputScan = 9

	// dateInputTestCassandraCompactionIsIdempotent is the date input test cassandra
	// compaction is idempotent the tests use.
	dateInputTestCassandraCompactionIsIdempotent = 5

	// dateInputToken is the date input token the tests use.
	dateInputToken = 10

	// dateInputTotals is the date input totals the tests use.
	dateInputTotals = 2025

	// dateInputWriteCompactionFacts is the date input write compaction facts the tests use.
	dateInputWriteCompactionFacts = 4

	// compactionFactsHour is the date input write compaction facts13 the tests use.
	compactionFactsHour = 9

	// equalInput is the equal input the tests use.
	equalInput = 43

	// equalInputIdempotent is the equal input idempotent the tests use.
	equalInputIdempotent = 2

	// equalInputTotals is the equal input totals the tests use.
	equalInputTotals = 2

	// factInput is the fact input the tests use.
	factInput = 17

	// factInputIdempotent is the fact input idempotent the tests use.
	factInputIdempotent = 100

	// factInputMeasures is the fact input measures the tests use.
	factInputMeasures = 25

	// initialWant is the initial want the tests use.
	initialWant = 0.0

	// intN is the int n the tests use.
	intN = 2

	// iterations is the iterations the tests use.
	iterations = 80

	// iterationsFacts is the iterations facts the tests use.
	iterationsFacts = 43

	// iterationsFilters is the iterations filters the tests use.
	iterationsFilters = 30

	// iterationsToken is the iterations token the tests use.
	iterationsToken = 5

	// lenInput is the len input the tests use.
	lenInput = 2

	// makeInput is the make input the tests use.
	makeInput = 80

	// makeInputFacts is the make input facts the tests use.
	makeInputFacts = 43

	// makeInputToken is the make input token the tests use.
	makeInputToken = 5

	// newSequenceInput is the new sequence input the tests use.
	newSequenceInput = 42

	// newStoreInput is the new store input the tests use.
	newStoreInput = 7

	// newStoreInputToken is the new store input token the tests use.
	newStoreInputToken = 2

	// upperBound is the upper bound the tests use.
	upperBound = 2

	// upperBoundFilter is the upper bound filter the tests use.
	upperBoundFilter = 4

	// floatTolerance is the named value used where float tolerance applies.
	floatTolerance = 1e-9

	// wantIndex is the expected index.
	wantIndex = 40

	// wantResult is the expected result.
	wantResult = 30.0

	// wantResultBoundaries is the expected result boundaries.
	wantResultBoundaries = 2.0

	// wantResultIdempotent is the expected result idempotent.
	wantResultIdempotent = 41.0

	// wantResultIsNoop is the expected result is noop.
	wantResultIsNoop = 1.0

	// wantResultLoseMeasures is the expected result lose measures.
	wantResultLoseMeasures = 1.0

	// wantResultMeasures is the expected result measures.
	wantResultMeasures = 25.0

	// wantResultNoop is the expected result noop.
	wantResultNoop = 17.0
)
