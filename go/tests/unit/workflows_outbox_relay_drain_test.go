package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/outbox"
)

// drainBatch is the BatchSize the drain tests claim with.
const drainBatch = 2

// drainConfig is a relay config claiming drainBatch rows per pass, sending them in one
// chunk, with the given drain budget.
func drainConfig(budget time.Duration) outbox.RelayConfig {
	cfg := relayConfig(drainBatch, 1, maxAttempts5)

	cfg.BatchSize = drainBatch
	cfg.DrainBudget = budget

	return cfg
}

// expectSent makes the sink accept recs in one Send and the store mark each sent.
func expectSent(
	store *mocks.MockOutboxStore,
	sink *mocks.MockOutboxSink,
	recs []types.OutboxRecord,
) {
	sink.EXPECT().Send(gomock.Any(), recs).Return(make([]error, len(recs)))

	for i := range recs {
		store.EXPECT().MarkSent(gomock.Any(), recs[i].ID).Return(nil)
	}
}

// drainMetrics returns a metrics mock whose counters and gauges accept anything, and
// the outbox_relay_passes histogram it hands out.
func drainMetrics(ctrl *gomock.Controller) (*mocks.MockMetrics, *mocks.MockHistogram) {
	metrics := mocks.NewMockMetrics(ctrl)
	counter := mocks.NewMockCounter(ctrl)
	counter.EXPECT().Inc(gomock.Any()).AnyTimes()

	gauge := mocks.NewMockGauge(ctrl)
	gauge.EXPECT().Set(gomock.Any(), gomock.Any()).AnyTimes()
	metrics.EXPECT().
		Counter(gomock.Any(), gomock.Any(), textLane).
		AnyTimes().
		Return(counter)
	metrics.EXPECT().Gauge(gomock.Any(), gomock.Any(), textLane).AnyTimes().Return(gauge)

	passes := mocks.NewMockHistogram(ctrl)
	metrics.EXPECT().
		Histogram("outbox_relay_passes", gomock.Any(), gomock.Any(), textLane).
		Return(passes)

	return metrics, passes
}

// TestRelayConfig_DrainBudget tests the drain budget's default and validation.
//
// Why this test is important:
//   - The default must keep today's one-claim pass, and a negative budget is a config
//     mistake that must stop boot rather than silently disable draining
//
// What it tests:
//   - DefaultRelayConfig has a zero DrainBudget and validates
//   - a negative DrainBudget fails Validate with CodeInvalidInput
func TestRelayConfig_DrainBudget(t *testing.T) {
	t.Parallel()

	cfg := outbox.DefaultRelayConfig(textAudit)
	assert.Zero(t, cfg.DrainBudget)
	require.NoError(t, cfg.Validate())

	cfg.DrainBudget = -time.Second

	err := cfg.Validate()
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// TestRelay_DrainsUntilAShortClaim tests that a drain budget keeps claiming while
// claims come back full.
//
// Why this test is important:
//   - A relay that claims one batch per tick falls behind any burst larger than a
//     batch per tick, and the backlog's lag grows without bound
//
// What it tests:
//   - with BatchSize 2 and a one-minute budget, claims of 2, 2, 2 and 1 rows are all
//     delivered in one RunOnce (four claims), and outbox_relay_passes observes 4 for
//     the lane
func TestRelay_DrainsUntilAShortClaim(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockOutboxStore(ctrl)
	sink := mocks.NewMockOutboxSink(ctrl)
	metrics, passes := drainMetrics(ctrl)
	batches := [][]types.OutboxRecord{
		outboxRecords(
			outboxRecordsArg2,
			1,
		),
		outboxRecords(outboxRecordsArg2, 1),
		outboxRecords(outboxRecordsArg2, 1),
		outboxRecords(1, 1),
	}
	claims := make([]any, 0, len(batches))

	for _, batch := range batches {
		claims = append(
			claims,
			store.EXPECT().Claim(gomock.Any(), textAudit, 0, 2).Return(batch, nil),
		)
		expectSent(store, sink, batch)
	}

	gomock.InOrder(claims...)
	store.EXPECT().Stats(gomock.Any(), textAudit).Return(types.OutboxStats{}, nil)
	passes.EXPECT().Observe(observeArg4p0, textAudit)

	relay := outbox.NewRelay(drainConfig(time.Minute), relayTx(t), store, sink, nil,
		metrics, nil, fixedClock())

	require.NoError(t, relay.RunOnce(context.Background()))
}

// TestRelay_DrainStopsWhenTheBudgetElapses tests that the budget bounds one run.
//
// Why this test is important:
//   - An unbounded drain under a steady flood would never return, so the loop would
//     never refresh its gauges or yield to shutdown
//
// What it tests:
//   - with every claim full and the clock advancing 40s per claim against a 60s
//     budget, the run stops after the batch in flight when the budget ran out: two
//     claims, both delivered
func TestRelay_DrainStopsWhenTheBudgetElapses(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockOutboxStore(ctrl)
	sink := mocks.NewMockOutboxSink(ctrl)
	now := relayNow()
	clock := func() time.Time { return now }

	for range iterations2 {
		batch := outboxRecords(outboxRecordsArg2, 1)

		store.EXPECT().Claim(gomock.Any(), textAudit, 0, 2).DoAndReturn(
			func(context.Context, string, int, int) ([]types.OutboxRecord, error) {
				now = now.Add(40 * time.Second)

				return batch, nil
			})
		expectSent(store, sink, batch)
	}

	relay := outbox.NewRelay(drainConfig(time.Minute), relayTx(t), store, sink, nil,
		nil, nil, outbox.WithClock(clock))

	require.NoError(t, relay.RunOnce(context.Background()))
}

// TestRelay_ZeroBudgetClaimsOnce tests that the default keeps today's single claim.
//
// Why this test is important:
//   - Draining is opt-in; a relay without a budget must behave exactly as before
//
// What it tests:
//   - with DrainBudget 0 and a full claim, RunOnce claims once
func TestRelay_ZeroBudgetClaimsOnce(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockOutboxStore(ctrl)
	sink := mocks.NewMockOutboxSink(ctrl)
	batch := outboxRecords(outboxRecordsArg2, 1)
	store.EXPECT().Claim(gomock.Any(), textAudit, 0, 2).Return(batch, nil).Times(1)
	expectSent(store, sink, batch)

	relay := outbox.NewRelay(drainConfig(0), relayTx(t), store, sink, nil, nil, nil,
		fixedClock())

	require.NoError(t, relay.RunOnce(context.Background()))
}

// TestRelay_DrainStopsOnTheFirstError tests that a failing pass ends the drain.
//
// Why this test is important:
//   - When finalizes fail (the database is struggling), claiming more rows only adds
//     load and leaves more rows leased until their leases expire
//
// What it tests:
//   - a full first claim whose finalize fails returns that error coded, with no
//     second claim, despite a one-minute budget
func TestRelay_DrainStopsOnTheFirstError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockOutboxStore(ctrl)
	sink := mocks.NewMockOutboxSink(ctrl)
	batch := outboxRecords(outboxRecordsArg2, 1)
	store.EXPECT().Claim(gomock.Any(), textAudit, 0, 2).Return(batch, nil).Times(1)
	sink.EXPECT().Send(gomock.Any(), batch).Return(make([]error, size2))
	store.EXPECT().MarkSent(gomock.Any(), batch[0].ID).Return(nil)
	store.EXPECT().MarkSent(gomock.Any(), batch[1].ID).
		Return(apperr.New(apperr.CodeUnavailable, "db down"))

	relay := outbox.NewRelay(drainConfig(time.Minute), relayTx(t), store, sink, nil,
		nil, nil, fixedClock())

	err := relay.RunOnce(context.Background())
	require.Error(t, err)
	assert.Equal(t, apperr.CodeUnavailable, apperr.Code(err))
}

// TestRelay_DrainStopsWhenTheContextEnds tests that shutdown ends the drain.
//
// Why this test is important:
//   - A drain that ignored cancellation would hold a terminating pod until its
//     budget ran out
//
// What it tests:
//   - a context cancelled during the first (full) pass's delivery ends the run after
//     that pass, with a single claim
func TestRelay_DrainStopsWhenTheContextEnds(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockOutboxStore(ctrl)
	sink := mocks.NewMockOutboxSink(ctrl)
	ctx, cancel := context.WithCancel(context.Background())

	defer cancel()

	batch := outboxRecords(outboxRecordsArg2, 1)
	store.EXPECT().Claim(gomock.Any(), textAudit, 0, 2).Return(batch, nil).Times(1)
	sink.EXPECT().Send(gomock.Any(), batch).DoAndReturn(
		func(context.Context, []types.OutboxRecord) []error {
			cancel()

			return make([]error, 2)
		})
	store.EXPECT().MarkSent(gomock.Any(), gomock.AssignableToTypeOf(uuid.UUID{})).
		Return(nil).Times(timesArg2)

	relay := outbox.NewRelay(drainConfig(time.Minute), relayTx(t), store, sink, nil,
		nil, nil, fixedClock())

	require.NoError(t, relay.RunOnce(ctx))
}

// compile-time check that the drain helpers use the shared interfaces.
var _ interfaces.Histogram = (*mocks.MockHistogram)(nil)
