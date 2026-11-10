package unit_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/outbox"
)

// relayNow returns the fixed clock every relay test runs at.
func relayNow() time.Time {
	return time.Date(
		dateInput,
		dateInputRelayNow,
		relayNowHour,
		relayNowMinute,
		0,
		0,
		0,
		time.UTC,
	)
}

// relayConfig is a valid relay config for lane "audit" with the given batch shape.
func relayConfig(sendBatch, concurrency, maxAttempts int) outbox.RelayConfig {
	return fixtures.Build(func(row *outbox.RelayConfig) {
		row.Lane = textAudit
		row.BatchSize = batchSize
		row.SendBatch = sendBatch
		row.Concurrency = concurrency
		row.MaxAttempts = maxAttempts
		row.BaseBackoff = time.Second
		row.MaxBackoff = maxBackoff * time.Second
	})
}

// outboxRecords builds n claimed records of lane "audit" with the given attempt count.
func outboxRecords(n, attempts int) []types.OutboxRecord {
	recs := make([]types.OutboxRecord, n)
	for i := range recs {
		recs[i] = types.OutboxRecord{
			ID: uuid.New(), Tenant: textOrg1, Lane: textAudit, Payload: []byte{byte(i)},
			Attempts: attempts, CreatedAt: relayNow(),
			Attributes: nil,
			Key:        "",
		}
	}

	return recs
}

// relayTx returns a transaction manager mock that runs every transaction's body on a
// context marked in-transaction and commits it.
func relayTx(t *testing.T) *mocks.MockTransactionManager {
	t.Helper()

	transactionManager := mocks.NewMockTransactionManager(gomock.NewController(t))
	transactionManager.EXPECT().
		WithTransaction(gomock.Any(), gomock.Any()).
		AnyTimes().
		DoAndReturn(
			func(ctx context.Context, fn func(context.Context) error) error {
				return fn(interfaces.WithInTx(ctx))
			})

	return transactionManager
}

// TestRelay_CommitsClaimAndEachFinalizeAndDeliversOutsideATransaction tests the
// relay's transaction boundaries.
//
// Why this test is important:
//   - A write that autocommits outside a transaction escapes its workflow, and a
//     remote call made inside one holds row locks for as long as the sink takes
//   - One row's finalize must never roll back another's
//
// What it tests:
//   - the claim and each of the two finalizes run in their own transaction (three
//     transactions), while the sink's Send runs on a context with none open
//   - a finalize whose commit fails is returned coded and counted on no outcome,
//     while the other row's finalize still commits and is counted sent
//   - the run observes one pass on outbox_relay_passes{audit}
func TestRelay_CommitsClaimAndEachFinalizeAndDeliversOutsideATransaction(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockOutboxStore(ctrl)
	sink := mocks.NewMockOutboxSink(ctrl)
	metrics := mocks.NewMockMetrics(ctrl)
	counters := map[string]*mocks.MockCounter{}

	testRelayCommitsClaimAndEachFinalizeAndDeliversOutsideATransactionExpectsMetrics(
		t,
		ctrl,
		store,
		sink,
		metrics,
		counters,
	)
}

// testRelayCommitsClaimAndEachFinalizeAndDeliversOutsideATransactionExpectsMetrics
// continues testRelayCommitsClaimAndEachFinalizeAndDeliversOutsideATransaction:
// expects metrics.
func testRelayCommitsClaimAndEachFinalizeAndDeliversOutsideATransactionExpectsMetrics(
	t *testing.T,
	ctrl *gomock.Controller,
	store *mocks.MockOutboxStore,
	sink *mocks.MockOutboxSink,
	metrics *mocks.MockMetrics,
	counters map[string]*mocks.MockCounter,
) {
	t.Helper()

	metrics.EXPECT().Counter(gomock.Any(), gomock.Any(), textLane).AnyTimes().DoAndReturn(
		func(name, _ string, _ ...string) interfaces.Counter {
			c := mocks.NewMockCounter(ctrl)

			counters[name] = c

			return c
		})

	gauge := mocks.NewMockGauge(ctrl)
	gauge.EXPECT().Set(0.0, textAudit).Times(wantCalls) // depth and lag
	metrics.EXPECT().Gauge(gomock.Any(), gomock.Any(), textLane).AnyTimes().Return(gauge)

	passes := mocks.NewMockHistogram(ctrl)
	passes.EXPECT().Observe(observeInputExpectsMetrics, textAudit)
	metrics.EXPECT().
		Histogram("outbox_relay_passes", gomock.Any(), gomock.Any(), textLane).
		Return(passes)

	testRelayCommitsClaimAndFinalizeExpectsTransactions(
		t,
		ctrl,
		store,
		sink,
		metrics,
		counters,
	)
}

// testRelayCommitsClaimAndFinalizeExpectsTransactions continues
// testRelayCommitsClaimAndEachFinalizeAndDeliversOutsideATransaction: expects
// transactions.
func testRelayCommitsClaimAndFinalizeExpectsTransactions(
	t *testing.T,
	ctrl *gomock.Controller,
	store *mocks.MockOutboxStore,
	sink *mocks.MockOutboxSink,
	metrics *mocks.MockMetrics,
	counters map[string]*mocks.MockCounter,
) {
	t.Helper()

	recs := outboxRecords(outboxRecordsInput, 1)
	commitErr := apperr.New(apperr.CodeUnavailable, "commit lost")
	transactionManager := mocks.NewMockTransactionManager(ctrl)

	var txs int

	transactionManager.EXPECT().
		WithTransaction(gomock.Any(), gomock.Any()).
		Times(3).
		DoAndReturn(
			func(ctx context.Context, fn func(context.Context) error) error {
				txs++

				err := fn(interfaces.WithInTx(ctx))
				if err != nil {
					return err
				}

				if txs == 3 {
					return commitErr // the second row's finalize fails to commit
				}

				return nil
			})

	testRelayCommitsClaimAndFinalizeExpectsClaimAndSend(
		t,
		store,
		sink,
		metrics,
		counters,
		recs,
		transactionManager,
		&txs,
	)
}

// testRelayCommitsClaimAndFinalizeExpectsClaimAndSend continues
// testRelayCommitsClaimAndFinalizeExpectsTransactions: expects claim and send.
func testRelayCommitsClaimAndFinalizeExpectsClaimAndSend(
	t *testing.T,
	store *mocks.MockOutboxStore,
	sink *mocks.MockOutboxSink,
	metrics *mocks.MockMetrics,
	counters map[string]*mocks.MockCounter,
	recs []types.OutboxRecord,
	transactionManager *mocks.MockTransactionManager,
	txs *int,
) {
	t.Helper()

	inTx := interfaces.InTx

	store.EXPECT().Claim(gomock.Any(), textAudit, 0, 100).DoAndReturn(
		func(ctx context.Context, _ string, _, _ int) ([]types.OutboxRecord, error) {
			require.True(t, inTx(ctx), "the claim commits in a transaction")

			return recs, nil
		})
	sink.EXPECT().Send(gomock.Any(), recs).DoAndReturn(
		func(ctx context.Context, _ []types.OutboxRecord) []error {
			require.False(t, inTx(ctx), "delivery runs with no transaction open")

			return make([]error, len(recs))
		})

	for i := range recs {
		r := &recs[i]

		store.EXPECT().MarkSent(gomock.Any(), r.ID).DoAndReturn(
			func(ctx context.Context, _ uuid.UUID) error {
				require.True(t, inTx(ctx), "each finalize commits in a transaction")

				return nil
			})
	}

	store.EXPECT().
		Stats(gomock.Any(), textAudit).
		Return(stdx.Zero[types.OutboxStats](), nil)

	relay := outbox.NewRelay(
		new(relayConfig(relayConfigInputSend, 1, relayConfigInput)),
		transactionManager,
		store,
		sink,
		nil,
		metrics,
		nil,
		fixedClock(),
	)

	counters["outbox_sent_total"].EXPECT().Inc(textAudit).Times(1)

	err := relay.RunOnce(context.Background())
	require.Error(t, err)
	assert.Equal(t, apperr.CodeUnavailable, apperr.Code(err))
	assert.Equal(t, wantTxs, (*txs))
}

// fixedClock returns a relay option pinning the clock to relayNow.
func fixedClock() outbox.Option {
	return outbox.WithClock(relayNow)
}

// TestRelay_MarksSentOnSuccess tests that every delivered record is marked sent.
//
// Why this test is important:
//   - A delivered row left pending is redelivered forever; the relay's whole job
//     is to move each delivered row to sent exactly once
//
// What it tests:
//   - five claimed records with SendBatch 2 reach the sink as the chunks
//     [0,1], [2,3], [4] (each exactly once), every id is MarkSent exactly once,
//     nothing is retried or parked, Stats is not queried (metrics are off), and
//     RunOnce returns nil
func TestRelay_MarksSentOnSuccess(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockOutboxStore(ctrl)
	sink := mocks.NewMockOutboxSink(ctrl)
	recs := outboxRecords(outboxRecordsInputSuccess, 1)
	ctx := context.Background()

	store.EXPECT().Claim(gomock.Any(), textAudit, 0, 100).Return(recs, nil).Times(1)

	for _, chunk := range [][]types.OutboxRecord{
		recs[0:offsetSuccess], recs[offsetSuccess:offsetOnSuccess],
		recs[offsetOnSuccess:offsetSentOnSuccess],
	} {
		sink.EXPECT().Send(gomock.Any(), chunk).Return(make([]error, len(chunk))).Times(1)
	}

	for _, r := range recs {
		store.EXPECT().MarkSent(gomock.Any(), r.ID).Return(nil).Times(1)
	}

	relay := outbox.NewRelay(
		new(
			relayConfig(
				relayConfigInputSuccess,
				relayConfigInputSuccess,
				relayConfigInput,
			),
		),
		relayTx(t),
		store,
		sink,
		nil,
		nil,
		nil,
		fixedClock(),
	)

	require.NoError(t, relay.RunOnce(ctx))
}

// TestRelay_PartialBatchFailureRetriesOnlyFailedRows tests per-record outcomes.
//
// Why this test is important:
//   - A batch sink can deliver some entries and reject others; retrying the
//     delivered ones duplicates them, dropping the failed ones loses them
//
// What it tests:
//   - sink results [nil, err, nil] mark records 0 and 2 sent and retry only
//     record 1, with the sink error's text as lastErr
//   - a sink that returns the wrong number of results fails every record of the
//     chunk (all retried), since no outcome can be attributed
func TestRelay_PartialBatchFailureRetriesOnlyFailedRows(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockOutboxStore(ctrl)
	sink := mocks.NewMockOutboxSink(ctrl)
	recs := outboxRecords(outboxRecordsInputRows, 1)
	sendErr := apperr.New(apperr.CodeUnavailable, "queue throttled")
	ctx := context.Background()
	noJitter := outbox.WithJitter(func(d time.Duration) time.Duration { return d })

	store.EXPECT().Claim(gomock.Any(), textAudit, 0, 100).Return(recs, nil)
	sink.EXPECT().Send(gomock.Any(), recs).Return([]error{nil, sendErr, nil})
	store.EXPECT().MarkSent(gomock.Any(), recs[0].ID).Return(nil)
	store.EXPECT().MarkSent(gomock.Any(), recs[2].ID).Return(nil)
	store.EXPECT().
		Retry(gomock.Any(), recs[1].ID, relayNow().Add(2*time.Second), sendErr.Error()).
		Return(nil)

	relay := outbox.NewRelay(
		new(relayConfig(relayConfigInputRows, 1, relayConfigInput)),
		relayTx(t),
		store,
		sink,
		nil,
		nil,
		nil,
		fixedClock(),
		noJitter,
	)
	require.NoError(t, relay.RunOnce(ctx))

	short := outboxRecords(outboxRecordsInput, 1)
	store.EXPECT().Claim(gomock.Any(), textAudit, 0, 100).Return(short, nil)
	sink.EXPECT().Send(gomock.Any(), short).Return([]error{nil})

	for _, r := range short {
		store.EXPECT().Retry(gomock.Any(), r.ID, relayNow().Add(2*time.Second),
			"INTERNAL: outbox relay: sink returned 1 results for 2 records").Return(nil)
	}

	require.NoError(t, relay.RunOnce(ctx))
}

// TestRelay_BackoffIsExponentialWithJitterAndCapped tests the retry schedule.
//
// Why this test is important:
//   - A failing destination must be backed off exponentially (not hammered),
//     never beyond the cap (or a recovered sink waits hours), and with jitter so
//     a fleet of relays does not retry in lock-step
//
// What it tests:
//   - with Base 1s and Max 10s, failed records at attempts 1, 2, 3, 4 hand the
//     jitter caps of 2s, 4s, 8s and 10s (16s capped) and are retried at now + the
//     jittered value (here half the cap)
//   - with the default full jitter, the next attempt falls in [now, now+cap]
func TestRelay_BackoffIsExponentialWithJitterAndCapped(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockOutboxStore(ctrl)
	sink := mocks.NewMockOutboxSink(ctrl)
	ctx := context.Background()
	sendErr := apperr.New(apperr.CodeUnavailable, textDown)

	var recs []types.OutboxRecord

	for attempts := 1; attempts <= wantAttemptsCapped; attempts++ {
		recs = append(recs, outboxRecords(1, attempts)...)
	}

	testRelayBackoffIsExponentialWithJitterAndCappedExpectsRetry(
		ctx, t, store, sink, sendErr, recs,
	)
}

// testRelayBackoffIsExponentialWithJitterAndCappedExpectsRetry continues
// testRelayBackoffIsExponentialWithJitterAndCapped: expects retry.
func testRelayBackoffIsExponentialWithJitterAndCappedExpectsRetry(
	ctx context.Context,
	t *testing.T,
	store *mocks.MockOutboxStore,
	sink *mocks.MockOutboxSink,
	sendErr *apperr.AppError,
	recs []types.OutboxRecord,
) {
	t.Helper()

	var (
		lock sync.Mutex
		caps []time.Duration
	)

	half := outbox.WithJitter(func(duration time.Duration) time.Duration {
		lock.Lock()
		defer lock.Unlock()

		caps = append(caps, duration)

		return duration / 2
	})

	store.EXPECT().Claim(gomock.Any(), textAudit, 0, 100).Return(recs, nil)
	sink.EXPECT().
		Send(gomock.Any(), recs).
		Return([]error{sendErr, sendErr, sendErr, sendErr})

	testRelayBackoffIsExponentialWithJitterAndCappedChecksBackoff(
		ctx, t, store, sink, sendErr, recs, &caps, half,
	)
}

// testRelayBackoffIsExponentialWithJitterAndCappedChecksBackoff continues
// testRelayBackoffIsExponentialWithJitterAndCappedExpectsRetry: checks backoff.
func testRelayBackoffIsExponentialWithJitterAndCappedChecksBackoff(
	ctx context.Context,
	t *testing.T,
	store *mocks.MockOutboxStore,
	sink *mocks.MockOutboxSink,
	sendErr *apperr.AppError,
	recs []types.OutboxRecord,
	caps *[]time.Duration,
	half outbox.Option,
) {
	t.Helper()

	wantNext := []time.Duration{
		time.Second,
		seconds * time.Second,
		secondsBackoff * time.Second,
		secondsChecksBackoff * time.Second,
	}

	for i := range recs {
		r := &recs[i]

		store.EXPECT().
			Retry(gomock.Any(), r.ID, relayNow().Add(wantNext[i]), "UNAVAILABLE: down").
			Return(nil)
	}

	relay := outbox.NewRelay(
		new(relayConfig(relayConfigInputBackoff, 1, relayConfigInputBackoff)),
		relayTx(t),
		store,
		sink,
		nil,
		nil,
		nil,
		fixedClock(),
		half,
	)
	require.NoError(t, relay.RunOnce(ctx))
	assert.Equal(
		t,
		[]time.Duration{
			2 * time.Second,
			4 * time.Second,
			8 * time.Second,
			10 * time.Second,
		},
		(*caps),
	)

	testRelayBackoffIsExponentialWithJitterAndCappedDefaultJitter(
		ctx,
		t,
		store,
		sink,
		sendErr,
	)
}

// testRelayBackoffIsExponentialWithJitterAndCappedDefaultJitter continues
// TestRelay_BackoffIsExponentialWithJitterAndCapped: default jitter.
func testRelayBackoffIsExponentialWithJitterAndCappedDefaultJitter(
	ctx context.Context,
	t *testing.T,
	store *mocks.MockOutboxStore,
	sink *mocks.MockOutboxSink,
	sendErr *apperr.AppError,
) {
	t.Helper()

	one := outboxRecords(1, outboxRecordsInputJitter)
	store.EXPECT().Claim(gomock.Any(), textAudit, 0, 100).Return(one, nil)
	sink.EXPECT().Send(gomock.Any(), one).Return([]error{sendErr})

	inWindow := gomock.Cond(func(x time.Time) bool {
		return !x.Before(relayNow()) && !x.After(relayNow().Add(8*time.Second))
	})
	store.EXPECT().
		Retry(gomock.Any(), one[0].ID, inWindow, "UNAVAILABLE: down").
		Return(nil)

	defaultJitter := outbox.NewRelay(
		new(relayConfig(relayConfigInputJitter, 1, relayConfigInputJitter)),
		relayTx(t),
		store,
		sink,
		nil,
		nil,
		nil,
		fixedClock(),
	)
	require.NoError(t, defaultJitter.RunOnce(ctx))
}

// TestRelay_ParksAfterMaxAttempts tests that an exhausted record is parked.
//
// Why this test is important:
//   - A poison record (one the destination always rejects) must leave rotation
//     after MaxAttempts so it neither blocks the lane nor retries forever
//
// What it tests:
//   - with MaxAttempts 3, a failed record on its 3rd attempt is Parked with the
//     error text and never retried, while a failed record on its 2nd attempt is
//     retried, not parked
func TestRelay_ParksAfterMaxAttempts(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockOutboxStore(ctrl)
	sink := mocks.NewMockOutboxSink(ctrl)
	exhausted := outboxRecords(1, outboxRecordsInputAttempts)[0]
	young := outboxRecords(1, outboxRecordsInput)[0]
	recs := []types.OutboxRecord{exhausted, young}
	reject := apperr.New(apperr.CodeInvalidInput, "malformed payload")

	store.EXPECT().Claim(gomock.Any(), textAudit, 0, 100).Return(recs, nil)
	sink.EXPECT().Send(gomock.Any(), recs).Return([]error{reject, reject})
	store.EXPECT().Park(gomock.Any(), exhausted.ID, reject.Error()).Return(nil).Times(1)
	store.EXPECT().
		Retry(gomock.Any(), young.ID, gomock.Any(), reject.Error()).
		Return(nil).
		Times(1)

	relay := outbox.NewRelay(
		new(relayConfig(relayConfigInputAttempts, 1, relayConfigInputMaxAttempts)),
		relayTx(t),
		store,
		sink,
		nil,
		nil,
		nil,
		fixedClock(),
	)

	require.NoError(t, relay.RunOnce(context.Background()))
}

// TestRelay_ReportsDepthAndLag tests the lane gauges and outcome counters.
//
// Why this test is important:
//   - Depth and lag are the outbox's health signals: a stuck relay shows as a
//     growing backlog and an aging oldest row long before anyone notices
//     missing messages downstream
//
// What it tests:
//   - Stats{Pending 7, OldestPending now-90s} sets outbox_depth{audit}=7 and
//     outbox_lag_seconds{audit}=90
//   - one sent, one retried and one parked record increment outbox_sent_total,
//     outbox_retried_total and outbox_parked_total{audit} once each
//   - an empty lane (zero OldestPending) reports lag 0
//   - each run observes one pass on outbox_relay_passes{audit}
func TestRelay_ReportsDepthAndLag(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockOutboxStore(ctrl)
	sink := mocks.NewMockOutboxSink(ctrl)
	metrics := mocks.NewMockMetrics(ctrl)

	testRelayReportsDepthAndLagExpectsCounters(t, ctrl, store, sink, metrics)
}

// testRelayReportsDepthAndLagExpectsCounters continues testRelayReportsDepthAndLag:
// expects counters.
func testRelayReportsDepthAndLagExpectsCounters(
	t *testing.T,
	ctrl *gomock.Controller,
	store *mocks.MockOutboxStore,
	sink *mocks.MockOutboxSink,
	metrics *mocks.MockMetrics,
) {
	t.Helper()

	sent, retried, parked := mocks.NewMockCounter(
		ctrl,
	), mocks.NewMockCounter(
		ctrl,
	), mocks.NewMockCounter(
		ctrl,
	)
	depth, lag := mocks.NewMockGauge(ctrl), mocks.NewMockGauge(ctrl)
	ctx := context.Background()

	metrics.EXPECT().Counter("outbox_sent_total", gomock.Any(), textLane).Return(sent)
	metrics.EXPECT().
		Counter("outbox_retried_total", gomock.Any(), textLane).
		Return(retried)
	metrics.EXPECT().Counter("outbox_parked_total", gomock.Any(), textLane).Return(parked)
	metrics.EXPECT().Gauge("outbox_depth", gomock.Any(), textLane).Return(depth)
	metrics.EXPECT().Gauge("outbox_lag_seconds", gomock.Any(), textLane).Return(lag)

	passes := mocks.NewMockHistogram(ctrl)
	metrics.EXPECT().
		Histogram("outbox_relay_passes", gomock.Any(), gomock.Any(), textLane).
		Return(passes)
	passes.EXPECT().
		Observe(1.0, textAudit).
		Times(wantCalls)

		// one claim per run, two runs

	outboxRecord, retry, park := outboxRecords(1, 1)[0], outboxRecords(1, 1)[0],
		outboxRecords(1,
			outboxRecordsInput)[0]
	recs := []types.OutboxRecord{outboxRecord, retry, park}
	fail := apperr.New(apperr.CodeUnavailable, textDown)

	store.EXPECT().Claim(gomock.Any(), textAudit, 0, 100).Return(recs, nil)
	sink.EXPECT().Send(gomock.Any(), recs).Return([]error{nil, fail, fail})
	store.EXPECT().MarkSent(gomock.Any(), outboxRecord.ID).Return(nil)
	store.EXPECT().Retry(gomock.Any(), retry.ID, gomock.Any(), fail.Error()).Return(nil)
	store.EXPECT().Park(gomock.Any(), park.ID, fail.Error()).Return(nil)
	sent.EXPECT().Inc(textAudit).Times(1)
	retried.EXPECT().Inc(textAudit).Times(1)

	testRelayReportsDepthAndLagExpectsGauges(
		ctx,
		t,
		store,
		sink,
		metrics,
		parked,
		depth,
		lag,
	)
}

// testRelayReportsDepthAndLagExpectsGauges continues
// testRelayReportsDepthAndLagExpectsCounters: expects gauges.
func testRelayReportsDepthAndLagExpectsGauges(
	ctx context.Context,
	t *testing.T,
	store *mocks.MockOutboxStore,
	sink *mocks.MockOutboxSink,
	metrics *mocks.MockMetrics,
	parked *mocks.MockCounter,
	depth, lag *mocks.MockGauge,
) {
	t.Helper()

	parked.EXPECT().Inc(textAudit).Times(1)
	store.EXPECT().Stats(gomock.Any(), textAudit).
		Return(
			types.OutboxStats{
				Pending:       7,
				Parked:        1,
				OldestPending: relayNow().Add(-90 * time.Second),
				Stranded:      0,
			},
			nil,
		)
	depth.EXPECT().Set(float64(gaugeReadingGauges), textAudit)
	lag.EXPECT().Set(float64(gaugeReadingExpectsGauges), textAudit)

	relay := outbox.NewRelay(
		new(relayConfig(relayConfigInputGauges, 1, relayConfigInputExpectsGauges)),
		relayTx(t),
		store,
		sink,
		nil,
		metrics,
		nil,
		fixedClock(),
	)
	require.NoError(t, relay.RunOnce(ctx))

	store.EXPECT().Claim(gomock.Any(), textAudit, 0, 100).Return(nil, nil)
	store.EXPECT().
		Stats(gomock.Any(), textAudit).
		Return(stdx.Zero[types.OutboxStats](), nil)
	depth.EXPECT().Set(float64(0), textAudit)
	lag.EXPECT().Set(float64(0), textAudit)

	require.NoError(t, relay.RunOnce(ctx))
}

// TestRelay_ClaimErrorIsNotRetried tests that a failed claim is surfaced once.
//
// Why this test is important:
//   - Claim is side-effecting (it counts an attempt and leases the rows); a
//     blind retry could burn a record's attempts without ever sending it
//
// What it tests:
//   - a Claim error is called exactly once, nothing is sent or finalized, and
//     RunOnce returns an error carrying the claim's code (UNAVAILABLE)
func TestRelay_ClaimErrorIsNotRetried(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockOutboxStore(ctrl)
	sink := mocks.NewMockOutboxSink(ctrl)

	store.EXPECT().Claim(gomock.Any(), textAudit, 0, 100).
		Return(nil, apperr.New(apperr.CodeUnavailable, "db down")).Times(1)

	relay := outbox.NewRelay(
		new(relayConfig(relayConfigInputRetried, 1, relayConfigInputNotRetried)),
		relayTx(t),
		store,
		sink,
		nil,
		nil,
		nil,
		fixedClock(),
	)
	err := relay.RunOnce(context.Background())

	require.Error(t, err)
	assert.Equal(t, apperr.CodeUnavailable, apperr.Code(err))
}

// TestRelay_FinalizeErrorIsReturnedWithoutStoppingOtherRows tests finalize failures.
//
// Why this test is important:
//   - One row's MarkSent failing must not strand the rest of the batch; the row
//     itself is safe (its lease expires and it is re-claimed), but the caller
//     must learn of the failure
//
// What it tests:
//   - with MarkSent failing for record 0, record 1 is still MarkSent and RunOnce
//     returns the INTERNAL finalize error
func TestRelay_FinalizeErrorIsReturnedWithoutStoppingOtherRows(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockOutboxStore(ctrl)
	sink := mocks.NewMockOutboxSink(ctrl)
	recs := outboxRecords(outboxRecordsInput, 1)

	store.EXPECT().Claim(gomock.Any(), textAudit, 0, 100).Return(recs, nil)
	sink.EXPECT().Send(gomock.Any(), recs).Return([]error{nil, nil})
	store.EXPECT().
		MarkSent(gomock.Any(), recs[0].ID).
		Return(apperr.New(apperr.CodeInternal, "tx aborted"))
	store.EXPECT().MarkSent(gomock.Any(), recs[1].ID).Return(nil)

	relay := outbox.NewRelay(
		new(relayConfig(relayConfigInputRows, 1, relayConfigInputOtherRows)),
		relayTx(t),
		store,
		sink,
		nil,
		nil,
		nil,
		fixedClock(),
	)
	err := relay.RunOnce(context.Background())

	require.Error(t, err)
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
}

// TestRelayConfig_Validate tests the relay's config validation.
//
// Why this test is important:
//   - A zero batch, concurrency or attempt budget would make the relay spin
//     doing nothing or park every record on its first failure; such a config
//     must fail at the composition root, not in production
//
// What it tests:
//   - DefaultRelayConfig("audit") is batch 100, send 10, concurrency 4, park on
//     attempt 10, backoff 1s..5m, and validates
//   - a complete config validates; an empty lane, non-positive BatchSize,
//     SendBatch, Concurrency, MaxAttempts or BaseBackoff, and MaxBackoff below
//     BaseBackoff are each INVALID_INPUT
//   - NewRelay panics on an invalid config and on a nil transaction manager
func TestRelayConfig_Validate(t *testing.T) {
	t.Parallel()
	require.NoError(t, new(relayConfig(10, 1, 3)).Validate())
	assert.Equal(t, fixtures.Build(func(row *outbox.RelayConfig) {
		row.Lane = textAudit
		row.BatchSize = 100
		row.SendBatch = 10
		row.Concurrency = 4
		row.MaxAttempts = 10
		row.BaseBackoff = time.Second
		row.MaxBackoff = 5 * time.Minute
	}), outbox.DefaultRelayConfig(textAudit))
	require.NoError(t, new(outbox.DefaultRelayConfig(textAudit)).Validate())

	broken := map[string]func(*outbox.RelayConfig){
		textLane:        func(c *outbox.RelayConfig) { c.Lane = "" },
		"batch":         func(c *outbox.RelayConfig) { c.BatchSize = 0 },
		"send batch":    func(c *outbox.RelayConfig) { c.SendBatch = 0 },
		textConcurrency: func(c *outbox.RelayConfig) { c.Concurrency = 0 },
		"attempts":      func(c *outbox.RelayConfig) { c.MaxAttempts = 0 },
		textBase:        func(c *outbox.RelayConfig) { c.BaseBackoff = 0 },
		textMax:         func(c *outbox.RelayConfig) { c.MaxBackoff = time.Millisecond },
	}
	for name, mutate := range broken {
		cfg := relayConfig(relayConfigInputValidate, 1, relayConfigInputConfigValidate)
		mutate(&cfg)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(cfg.Validate()), name)
	}

	ctrl := gomock.NewController(t)
	assert.Panics(t, func() {
		outbox.NewRelay(
			new(outbox.RelayConfig),
			relayTx(t),
			mocks.NewMockOutboxStore(ctrl),
			mocks.NewMockOutboxSink(ctrl),
			nil,
			nil,
			nil,
		)
	})
	assert.Panics(t, func() {
		outbox.NewRelay(
			new(relayConfig(10, 1, 3)),
			nil,
			mocks.NewMockOutboxStore(ctrl),
			mocks.NewMockOutboxSink(ctrl),
			nil,
			nil,
			nil,
		)
	})
}

// TestRelay_SpansRunsAndLogsStatsFailure tests the relay's tracing and logging.
//
// Why this test is important:
//   - A failing run must be visible on its trace, and a failing Stats query
//     (which only feeds the gauges) must be logged rather than fail the run
//
// What it tests:
//   - a run whose claim fails opens span "outbox.relay.run" with attribute
//     outbox.lane=audit, records the error and sets status Error, then ends it
//   - a run whose Stats fails returns nil, leaves the gauges untouched and logs
//     one Warn "outbox relay: stats failed"
//   - only the run whose claim succeeded observes a pass on outbox_relay_passes
func TestRelay_SpansRunsAndLogsStatsFailure(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockOutboxStore(ctrl)
	sink := mocks.NewMockOutboxSink(ctrl)
	tracer, span := mocks.NewMockTracer(ctrl), mocks.NewMockSpan(ctrl)
	logger := mocks.NewMockLogger(ctrl)
	metrics := mocks.NewMockMetrics(ctrl)
	gauge := mocks.NewMockGauge(ctrl)
	ctx := context.Background()

	metrics.EXPECT().
		Counter(gomock.Any(), gomock.Any(), textLane).
		Return(mocks.NewMockCounter(ctrl)).
		Times(wantCallsFailure)
	metrics.EXPECT().
		Gauge(gomock.Any(), gomock.Any(), textLane).
		Return(gauge).
		Times(wantCalls)

	passes := mocks.NewMockHistogram(ctrl)
	metrics.EXPECT().
		Histogram("outbox_relay_passes", gomock.Any(), gomock.Any(), textLane).
		Return(passes)
	passes.EXPECT().
		Observe(1.0, textAudit).
		Times(1)

	testRelaySpansRunsAndLogsStatsFailureExpectsSpan(ctx,
		t,
		store,
		sink,
		tracer,
		span,
		logger,
		metrics,
	)
}

// testRelaySpansRunsAndLogsStatsFailureExpectsSpan continues
// TestRelay_SpansRunsAndLogsStatsFailure.
func testRelaySpansRunsAndLogsStatsFailureExpectsSpan(ctx context.Context,
	t *testing.T,
	store *mocks.MockOutboxStore,
	sink *mocks.MockOutboxSink,
	tracer *mocks.MockTracer,
	span *mocks.MockSpan,
	logger *mocks.MockLogger,
	metrics *mocks.MockMetrics,
) {
	t.Helper()

	// the second run; the first claim failed
	claimErr := apperr.New(apperr.CodeUnavailable, "db down")

	tracer.EXPECT().
		Start(gomock.Any(), "outbox.relay.run").
		Return(ctx, span).
		Times(wantCalls)
	span.EXPECT().SetAttribute("outbox.lane", textAudit).Times(wantCalls)
	span.EXPECT().RecordError(gomock.Any()).Times(1)
	span.EXPECT().SetStatus(interfaces.SpanStatusError, gomock.Any()).Times(1)
	span.EXPECT().End().Times(wantCalls)
	store.EXPECT().Claim(gomock.Any(), textAudit, 0, 100).Return(nil, claimErr)

	relay := outbox.NewRelay(
		new(relayConfig(relayConfigInputFailure, 1, relayConfigInputStatsFailure)),
		relayTx(t),
		store,
		sink,
		logger,
		metrics,
		tracer,
		fixedClock(),
	)
	require.Error(t, relay.RunOnce(ctx))

	store.EXPECT().Claim(gomock.Any(), textAudit, 0, 100).Return(nil, nil)
	store.EXPECT().
		Stats(gomock.Any(), textAudit).
		Return(stdx.Zero[types.OutboxStats](), claimErr)
	logger.EXPECT().WithContext(gomock.Any()).Return(logger)
	logger.EXPECT().
		Warn("outbox relay: stats failed", textLane, textAudit, textError, claimErr.Error()).
		Times(1)

	require.NoError(t, relay.RunOnce(ctx))
}

// TestRelay_CancelledRunLeavesFailedRowsLeased tests shutdown mid-delivery.
//
// Why this test is important:
//   - A relay stopped while a send is in flight sees that send fail with the
//     cancellation; recording it as a failed attempt would burn a retry (and
//     eventually park) a record that never had a real delivery failure
//
// What it tests:
//   - with the run's context cancelled during Send, a record whose send failed
//     with context.Canceled is neither retried nor parked, while a record the
//     sink delivered is still marked sent
func TestRelay_CancelledRunLeavesFailedRowsLeased(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockOutboxStore(ctrl)
	sink := mocks.NewMockOutboxSink(ctrl)
	recs := outboxRecords(outboxRecordsInput, 1)
	ctx, cancel := context.WithCancel(context.Background())

	defer cancel()

	store.EXPECT().Claim(gomock.Any(), textAudit, 0, 100).Return(recs, nil)
	sink.EXPECT().Send(gomock.Any(), recs).DoAndReturn(
		func(context.Context, []types.OutboxRecord) []error {
			cancel()

			return []error{
				nil,
				apperr.Wrap(context.Canceled, apperr.CodeCanceled, "send"),
			}
		})
	store.EXPECT().MarkSent(gomock.Any(), recs[0].ID).Return(nil).Times(1)

	relay := outbox.NewRelay(
		new(relayConfig(relayConfigInputLeased, 1, 1)),
		relayTx(t),
		store,
		sink,
		nil,
		nil,
		nil,
		fixedClock(),
	)

	require.NoError(t, relay.RunOnce(ctx))
}

// TestRelay_ParkOnPermanentParksAtFirstPermanentFailure tests ParkOnPermanent.
//
// Why this test is important:
//   - A record the destination rejects as malformed fails identically on every
//     attempt; retrying it MaxAttempts times only delays the operator seeing it
//
// What it tests:
//   - with ParkOnPermanent and MaxAttempts 5, a first-attempt INVALID_INPUT
//     failure is parked, while a first-attempt UNAVAILABLE failure is retried
//   - without ParkOnPermanent the same INVALID_INPUT failure is retried
func TestRelay_ParkOnPermanentParksAtFirstPermanentFailure(t *testing.T) {
	t.Parallel()

	reject := apperr.New(apperr.CodeInvalidInput, "malformed payload")
	outage := apperr.New(apperr.CodeUnavailable, "queue down")

	t.Run("enabled", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		store := mocks.NewMockOutboxStore(ctrl)
		sink := mocks.NewMockOutboxSink(ctrl)
		recs := outboxRecords(2, 1)
		store.EXPECT().Claim(gomock.Any(), textAudit, 0, 100).Return(recs, nil)
		sink.EXPECT().Send(gomock.Any(), recs).Return([]error{reject, outage})
		store.EXPECT().Park(gomock.Any(), recs[0].ID, reject.Error()).Return(nil).Times(1)
		store.EXPECT().
			Retry(gomock.Any(), recs[1].ID, gomock.Any(), outage.Error()).
			Return(nil).
			Times(1)

		cfg := relayConfig(10, 1, 5)

		cfg.ParkOnPermanent = true

		require.NoError(
			t,
			outbox.NewRelay(&cfg, relayTx(t), store, sink, nil, nil, nil, fixedClock()).
				RunOnce(context.Background()),
		)
	})

	testRelayParkOnPermanentParksAtFirstPermanentFailureDisabled(t, reject)
}

// testRelayParkOnPermanentParksAtFirstPermanentFailureDisabled continues
// TestRelay_ParkOnPermanentParksAtFirstPermanentFailure: disabled.
func testRelayParkOnPermanentParksAtFirstPermanentFailureDisabled(
	t *testing.T,
	reject *apperr.AppError,
) {
	t.Helper()

	t.Run("disabled", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		store := mocks.NewMockOutboxStore(ctrl)
		sink := mocks.NewMockOutboxSink(ctrl)
		recs := outboxRecords(1, 1)
		store.EXPECT().Claim(gomock.Any(), textAudit, 0, 100).Return(recs, nil)
		sink.EXPECT().Send(gomock.Any(), recs).Return([]error{reject})
		store.EXPECT().
			Retry(gomock.Any(), recs[0].ID, gomock.Any(), reject.Error()).
			Return(nil).
			Times(1)

		require.NoError(
			t,
			outbox.NewRelay(
				new(relayConfig(
					10,
					1,
					5,
				)),
				relayTx(t),
				store,
				sink,
				nil,
				nil,
				nil,
				fixedClock(),
			).RunOnce(context.Background()),
		)
	})
}

// TestLoadRelayConfig_OverlaysDefaultsAndRejectsInvalid tests the relay config loader.
//
// Why this test is important:
//   - NewRelay panics on an invalid config; composition roots load the config
//     through this function so a bad section stops boot with a coded error
//     instead of a panic
//
// What it tests:
//   - an absent section yields DefaultRelayConfig(lane)
//   - a section setting max_attempts 3 keeps the other defaults
//   - a section setting concurrency 0 is INVALID_INPUT
//   - an unmarshal failure is INVALID_INPUT reading "load outbox relay config"
func TestLoadRelayConfig_OverlaysDefaultsAndRejectsInvalid(t *testing.T) {
	t.Parallel()

	set := func(mut func(*outbox.RelayConfig)) func(string, any) error {
		return func(_ string, target any) error {
			mut(asType[*outbox.RelayConfig](t, target))

			return nil
		}
	}

	t.Run(textAbsent, func(t *testing.T) {
		t.Parallel()

		loader := mocks.NewMockConfigLoader(gomock.NewController(t))
		loader.EXPECT().Get("outbox.audit").Return(nil)

		cfg, err := outbox.LoadRelayConfig(loader, "outbox.audit", textAudit)

		require.NoError(t, err)
		assert.Equal(t, outbox.DefaultRelayConfig(textAudit), cfg)
	})

	t.Run("present", func(t *testing.T) {
		t.Parallel()

		loader := mocks.NewMockConfigLoader(gomock.NewController(t))
		loader.EXPECT().Get("outbox.audit").Return(map[string]any{"max_attempts": 3})
		loader.EXPECT().UnmarshalKey("outbox.audit", gomock.Any()).
			DoAndReturn(set(func(c *outbox.RelayConfig) { c.MaxAttempts = 3 }))

		cfg, err := outbox.LoadRelayConfig(loader, "outbox.audit", textAudit)

		require.NoError(t, err)

		want := outbox.DefaultRelayConfig(textAudit)

		want.MaxAttempts = 3
		assert.Equal(t, want, cfg)
	})

	testLoadRelayConfigOverlaysDefaultsAndRejectsInvalidInvalid(t, set)
}

// testLoadRelayConfigOverlaysDefaultsAndRejectsInvalidInvalid continues
// TestLoadRelayConfig_OverlaysDefaultsAndRejectsInvalid: invalid.
func testLoadRelayConfigOverlaysDefaultsAndRejectsInvalidInvalid(
	t *testing.T,
	set func(mut func(*outbox.RelayConfig)) func(string, any) error,
) {
	t.Helper()

	t.Run("invalid", func(t *testing.T) {
		t.Parallel()

		loader := mocks.NewMockConfigLoader(gomock.NewController(t))
		loader.EXPECT().Get("outbox.audit").Return(map[string]any{textConcurrency: 0})
		loader.EXPECT().UnmarshalKey("outbox.audit", gomock.Any()).
			DoAndReturn(set(func(c *outbox.RelayConfig) { c.Concurrency = 0 }))

		_, err := outbox.LoadRelayConfig(loader, "outbox.audit", textAudit)

		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	})

	t.Run("unmarshal failure", func(t *testing.T) {
		t.Parallel()

		loader := mocks.NewMockConfigLoader(gomock.NewController(t))
		loader.EXPECT().
			Get("outbox.audit").
			Return(map[string]any{textConcurrency: "many"})
		loader.EXPECT().
			UnmarshalKey("outbox.audit", gomock.Any()).
			Return(apperr.Sentinel("bad int"))

		_, err := outbox.LoadRelayConfig(loader, "outbox.audit", textAudit)

		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
		assert.Contains(t, err.Error(), "load outbox relay config")
	})
}

const (
	// batchSize is the value assigned to row.BatchSize in relayConfig.
	batchSize = 100

	// dateInputRelayNow is the month passed to Date in relayNow.
	dateInputRelayNow = 10

	// relayNowHour is the day passed to Date in relayNow.
	relayNowHour = 9

	// relayNowMinute is the hour passed to Date in relayNow.
	relayNowMinute = 12

	// gaugeReadingExpectsGauges is argument 1 of float64 in
	// testRelayReportsDepthAndLagExpectsGauges.
	gaugeReadingExpectsGauges = 90

	// gaugeReadingGauges is argument 1 of float64 in
	// testRelayReportsDepthAndLagExpectsGauges.
	gaugeReadingGauges = 7

	// maxBackoff is the factor multiplying time.Second in relayConfig.
	maxBackoff = 10

	// observeInputExpectsMetrics is the value passed to Observe in
	// testRelayCommitsClaimAndEachFinalizeAndDeliversOutsideATransactionExpectsMetrics.
	observeInputExpectsMetrics = 1.0

	// offsetOnSuccess is a slice bound of recs in TestRelay_MarksSentOnSuccess.
	offsetOnSuccess = 4

	// offsetSentOnSuccess is a slice bound of recs in TestRelay_MarksSentOnSuccess.
	offsetSentOnSuccess = 5

	// offsetSuccess is a slice bound of recs in TestRelay_MarksSentOnSuccess.
	offsetSuccess = 2

	// outboxRecordsInputAttempts is the attempts passed to outboxRecords in
	// TestRelay_ParksAfterMaxAttempts.
	outboxRecordsInputAttempts = 3

	// outboxRecordsInputJitter is the attempts passed to outboxRecords in
	// testRelayBackoffIsExponentialWithJitterAndCappedDefaultJitter.
	outboxRecordsInputJitter = 3

	// outboxRecordsInputRows is the n passed to outboxRecords in
	// TestRelay_PartialBatchFailureRetriesOnlyFailedRows.
	outboxRecordsInputRows = 3

	// outboxRecordsInputSuccess is the n passed to outboxRecords in
	// TestRelay_MarksSentOnSuccess.
	outboxRecordsInputSuccess = 5

	// relayConfigInputAttempts is the sendBatch passed to relayConfig in
	// TestRelay_ParksAfterMaxAttempts.
	relayConfigInputAttempts = 10

	// relayConfigInputBackoff is the maxAttempts passed to relayConfig in
	// testRelayBackoffIsExponentialWithJitterAndCappedChecksBackoff, and the sendBatch
	// passed to relayConfig in
	// testRelayBackoffIsExponentialWithJitterAndCappedChecksBackoff.
	relayConfigInputBackoff = 10

	// relayConfigInputConfigValidate is the relay config input config validate the tests
	// use.
	relayConfigInputConfigValidate = 3

	// relayConfigInputExpectsGauges is the maxAttempts passed to relayConfig in
	// testRelayReportsDepthAndLagExpectsGauges.
	relayConfigInputExpectsGauges = 2

	// relayConfigInputFailure is the sendBatch passed to relayConfig in
	// testRelaySpansRunsAndLogsStatsFailureExpectsSpan.
	relayConfigInputFailure = 10

	// relayConfigInputGauges is the sendBatch passed to relayConfig in
	// testRelayReportsDepthAndLagExpectsGauges.
	relayConfigInputGauges = 10

	// relayConfigInputJitter is the maxAttempts passed to relayConfig in
	// testRelayBackoffIsExponentialWithJitterAndCappedDefaultJitter, and the sendBatch
	// passed to relayConfig in
	// testRelayBackoffIsExponentialWithJitterAndCappedDefaultJitter.
	relayConfigInputJitter = 10

	// relayConfigInputLeased is the sendBatch passed to relayConfig in
	// TestRelay_CancelledRunLeavesFailedRowsLeased.
	relayConfigInputLeased = 10

	// relayConfigInputMaxAttempts is the maxAttempts passed to relayConfig in
	// TestRelay_ParksAfterMaxAttempts.
	relayConfigInputMaxAttempts = 3

	// relayConfigInputNotRetried is the maxAttempts passed to relayConfig in
	// TestRelay_ClaimErrorIsNotRetried.
	relayConfigInputNotRetried = 3

	// relayConfigInputOtherRows is the maxAttempts passed to relayConfig in
	// TestRelay_FinalizeErrorIsReturnedWithoutStoppingOtherRows.
	relayConfigInputOtherRows = 3

	// relayConfigInputRetried is the sendBatch passed to relayConfig in
	// TestRelay_ClaimErrorIsNotRetried.
	relayConfigInputRetried = 10

	// relayConfigInputRows is the sendBatch passed to relayConfig in
	// TestRelay_FinalizeErrorIsReturnedWithoutStoppingOtherRows and
	// TestRelay_PartialBatchFailureRetriesOnlyFailedRows.
	relayConfigInputRows = 10

	// relayConfigInputSend is the sendBatch passed to relayConfig in
	// testRelayCommitsClaimAndFinalizeExpectsClaimAndSend.
	relayConfigInputSend = 10

	// relayConfigInputStatsFailure is the maxAttempts passed to relayConfig in
	// testRelaySpansRunsAndLogsStatsFailureExpectsSpan.
	relayConfigInputStatsFailure = 3

	// relayConfigInputSuccess is the concurrency passed to relayConfig in
	// TestRelay_MarksSentOnSuccess, and the sendBatch passed to relayConfig in
	// TestRelay_MarksSentOnSuccess.
	relayConfigInputSuccess = 2

	// relayConfigInputValidate is the sendBatch passed to relayConfig in
	// TestRelayConfig_Validate.
	relayConfigInputValidate = 10

	// secondsBackoff is the factor multiplying time.Second in
	// testRelayBackoffIsExponentialWithJitterAndCappedChecksBackoff.
	secondsBackoff = 4

	// secondsChecksBackoff is the factor multiplying time.Second in
	// testRelayBackoffIsExponentialWithJitterAndCappedChecksBackoff.
	secondsChecksBackoff = 5

	// wantAttemptsCapped is the expected attempts capped.
	wantAttemptsCapped = 4

	// wantCallsFailure is the expected calls failure.
	wantCallsFailure = 3

	// wantTxs is the expected txs.
	wantTxs = 3
)

const (
	// textConcurrency is the literal "concurrency".
	textConcurrency = "concurrency"
)
