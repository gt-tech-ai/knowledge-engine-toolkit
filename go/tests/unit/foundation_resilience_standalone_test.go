package unit_test

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/metrics/prom"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/budget"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead/channel"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead/keyed"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/deadletter"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/dedup"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/ratelimiter"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/reswire"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/retry"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// standaloneRetries is the standalone retrier's MaxRetries: four attempts in all.
const standaloneRetries = 3

// standaloneRetryCase is one row of the standalone retrier's table.
type standaloneRetryCase struct {
	// failures is how many leading attempts fail transiently before one succeeds.
	failures int

	// budget is the retry budget the context carries, or observedNever for none.
	budget int32

	// want is what the retrier must record.
	want retryWant
}

// TestRetrier_RecordsStandaloneRetries tests that an exponential retrier handed a
// recorder counts its own retries, exhaustion and budget refusals, with no operation.
//
// Why this test is important:
//   - a retrier used outside a decorate tier (a worker loop, a one-off call) had no
//     layer to count for it; its retries were as invisible as the layers' were.
//
// What it tests:
//   - success first try: nothing; two failures then success: 2 retries; always
//     failing: 3 retries and 1 exhaustion; one retry then a budget refusal: 1 retry
//     and 1 budget_exhausted rejection, no exhaustion. Each under tier "standalone"
//     and operation "".
func TestRetrier_RecordsStandaloneRetries(t *testing.T) {
	t.Parallel()

	cases := map[string]standaloneRetryCase{
		"first try": {failures: 0, budget: observedNever, want: retryWant{0, 0, 0, 0}},
		"recovers": {
			failures: observedTwice, budget: observedNever,
			want: retryWant{observedTwice, 0, 0, 0},
		},
		"exhausted": {
			failures: standaloneRetries + 1, budget: observedNever,
			want: retryWant{standaloneRetries, 1, 0, 0},
		},
		"refused": {
			failures: standaloneRetries + 1, budget: 1,
			want: retryWant{1, 0, 1, 0},
		},
	}

	for name, row := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			harness := newStandalone(t)
			retrier, err := retry.New(retry.KindExponential,
				retry.WithMaxRetries(standaloneRetries),
				retry.WithInitialInterval(time.Millisecond),
				retry.WithMaxInterval(time.Millisecond),
				retry.WithRecorder(harness.obs.Recorder),
			)
			require.NoError(t, err)

			ctx := context.Background()
			if row.budget != observedNever {
				ctx = budget.WithRetryBudget(ctx, budget.NewBudget(row.budget))
			}

			calls := 0

			_ = retrier.Retry(ctx, func() error {
				_, err := failing(row.failures, &calls)(ctx)

				return err
			})

			requireRetries(t, &harness, "", row.want)
		})
	}
}

// requireRetries asserts the retries, exhaustion and budget rejections recorded under
// operation.
func requireRetries(
	t *testing.T,
	harness *layerHarness,
	operation string,
	want retryWant,
) {
	t.Helper()

	require.InDelta(t, want.retried, harness.value(t,
		"resilience_retries_total", observedOperation, operation), 0)
	require.InDelta(t, want.exhausted, harness.value(t,
		"resilience_retry_exhausted_total", observedOperation, operation), 0)
	require.InDelta(t, want.refused, harness.value(t,
		observedRejections, observedReason, "budget_exhausted"), 0)
}

// TestRetrier_WithoutRecorderRecordsNothing tests that a retrier with no recorder
// retries as before.
//
// Why this test is important:
//   - most retriers are built without metrics; counting must not touch a nil recorder.
//
// What it tests:
//   - a retrier with no recorder retries a twice-failing operation to success.
func TestRetrier_WithoutRecorderRecordsNothing(t *testing.T) {
	t.Parallel()

	retrier, err := retry.New(retry.KindExponential,
		retry.WithInitialInterval(time.Millisecond),
		retry.WithMaxInterval(time.Millisecond),
	)
	require.NoError(t, err)

	calls := 0

	require.NoError(t, retrier.Retry(context.Background(), func() error {
		_, failure := failing(observedTwice, &calls)(context.Background())

		return failure
	}))
	require.Equal(t, observedTwice+1, calls)
}

// The standalone token limiter's tuning.
const (
	// standaloneBurst is its burst.
	standaloneBurst = 3

	// standaloneRate is its refill, too slow to add a token during the test.
	standaloneRate = 0.001
)

// TestLimiter_RecordsStandaloneRefusals tests that a token limiter handed a recorder
// counts each Allow it refuses.
//
// Why this test is important:
//   - a limiter used outside a decorate tier (a background poller, a webhook sender)
//     refuses silently otherwise; the count is how an operator sees it throttle.
//
// What it tests:
//   - at burst 3 and a negligible refill, three Allow calls pass and the fourth counts
//     one rate_limited rejection under tier "standalone" and operation "".
func TestLimiter_RecordsStandaloneRefusals(t *testing.T) {
	t.Parallel()

	harness := newStandalone(t)
	limiter, err := ratelimiter.New(ratelimiter.KindToken,
		ratelimiter.WithRate(standaloneRate),
		ratelimiter.WithBurst(standaloneBurst),
		ratelimiter.WithRecorder(harness.obs.Recorder),
	)
	require.NoError(t, err)

	for range standaloneBurst {
		require.True(t, limiter.Allow())
	}

	require.False(t, limiter.Allow())
	require.InDelta(t, 1, harness.value(t,
		observedRejections, observedReason, "rate_limited"), 0)
	require.InDelta(t, 1, harness.value(t, observedRejections, observedOperation, ""), 0)
}

// TestChannelBulkhead_RecordsStandaloneRefusals tests that a channel bulkhead handed a
// recorder counts each TryExecute it refuses for want of a slot.
//
// Why this test is important:
//   - a standalone bulkhead that refuses work hides the overload behind its caller's
//     error handling; the count makes the refusals visible.
//
// What it tests:
//   - with its one slot held, TryExecute counts one bulkhead_full rejection; once the
//     slot is free it runs and counts none.
func TestChannelBulkhead_RecordsStandaloneRefusals(t *testing.T) {
	t.Parallel()

	harness := newStandalone(t)
	bulkhead := channel.New(
		channel.Config{Recorder: harness.obs.Recorder, MaxConcurrent: 1},
	)

	require.NoError(t, bulkhead.TryExecute(func() error {
		require.ErrorIs(t, bulkhead.TryExecute(func() error { return nil }),
			channel.ErrBulkheadFull)

		return nil
	}))
	require.NoError(t, bulkhead.TryExecute(func() error { return nil }))

	require.InDelta(t, 1, harness.value(t,
		observedRejections, observedReason, "bulkhead_full"), 0)
}

// TestKeyedLimiter_RecordsStandaloneRefusals tests that a keyed limiter handed a
// recorder counts each TryAcquire it refuses because the key holds its cap.
//
// Why this test is important:
//   - the keyed limiter caps one tenant's or one source's concurrency; a refusal is a
//     tenant being throttled, which an operator must be able to see.
//
// What it tests:
//   - at a cap of 1, a second TryAcquire on a held key counts one bulkhead_full
//     rejection (no key label: keys are unbounded); after release it succeeds and
//     counts none.
func TestKeyedLimiter_RecordsStandaloneRefusals(t *testing.T) {
	t.Parallel()

	harness := newStandalone(t)
	limiter, err := keyed.New(keyed.Config{Recorder: harness.obs.Recorder, MaxPerKey: 1})
	require.NoError(t, err)

	release, acquired := limiter.TryAcquire(observedOp)
	require.True(t, acquired)

	_, acquired = limiter.TryAcquire(observedOp)
	require.False(t, acquired)

	release()

	_, acquired = limiter.TryAcquire(observedOp)
	require.True(t, acquired)

	require.InDelta(t, 1, harness.value(t,
		observedRejections, observedReason, "bulkhead_full"), 0)
}

// standaloneThrottleAttempts is how many failing calls the throttle test makes; enough
// for the SRE throttler to start shedding.
const standaloneThrottleAttempts = 20

// TestAdaptiveThrottle_ReportsRatioAndSheds tests that an adaptive throttle built by
// reswire with metrics reports its rejection ratio and counts each call it sheds.
//
// Why this test is important:
//   - a throttler that sheds locally fails calls the dependency never sees; without
//     the ratio and the count an operator sees errors with no matching dependency
//     failures.
//
// What it tests:
//   - enabled: after a run of failures the ratio gauge is above 0 and at least one
//     call counts a throttled rejection under tier "standalone" and the given name.
//   - disabled: the same run records nothing.
func TestAdaptiveThrottle_ReportsRatioAndSheds(t *testing.T) {
	t.Parallel()

	for kind, records := range map[string]bool{"enabled": true, "disabled": false} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()

			harness := newStandalone(t)
			throttler := throttleOfKind(t, kind, &harness)

			failRepeatedly(throttler)

			ratio := harness.value(t, "resilience_throttle_rejection_ratio",
				textName, observedName)
			shed := harness.value(t, observedRejections, observedReason, "throttled")

			require.Equal(t, records, ratio > 0, "ratio %v", ratio)
			require.Equal(t, records, shed > 0, "shed %v", shed)
		})
	}
}

// failRepeatedly runs standaloneThrottleAttempts transiently failing calls through
// throttler.
func failRepeatedly(throttler interfaces.AdaptiveThrottler) {
	for range standaloneThrottleAttempts {
		_ = throttler.Do(context.Background(), func() error {
			return apperr.New(apperr.CodeUnavailable, observedDown)
		})
	}
}

// throttleOfKind builds the resilience.adaptive_throttle of kind through reswire with
// the harness's metrics under observedName.
func throttleOfKind(
	t *testing.T,
	kind string,
	harness *layerHarness,
) interfaces.AdaptiveThrottler {
	t.Helper()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, textBaseYaml),
		"resilience:\n  adaptive_throttle:\n    kind: "+kind+"\n")

	loader, err := config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err)

	throttler, err := reswire.NewAdaptiveThrottle(loader,
		reswire.WithMetrics(prom.NewFromRegistry(harness.registry), observedName))
	require.NoError(t, err)

	return throttler
}

// The hedge test's timings: the hedger fires its backup after standaloneHedgeDelay.
const (
	// standaloneHedgeDelay is the hedger's delay before it fires the backup.
	standaloneHedgeDelay = 20 * time.Millisecond

	// standaloneSlow is an attempt slower than the delay, but faster than a slower one.
	standaloneSlow = 2 * standaloneHedgeDelay

	// standaloneSlower is an attempt that loses any race it is in.
	standaloneSlower = 10 * standaloneHedgeDelay
)

// TestHedger_RecordsFiredAndWon tests that a hedger built by reswire with metrics counts
// each backup it fires and each backup whose result it returned.
//
// Why this test is important:
//   - hedging doubles load on the paths it covers; fired and won are how an operator
//     sees what that load buys: a backup that never wins is pure cost.
//
// What it tests:
//   - a primary faster than the delay: nothing; a slow primary that still beats its
//     backup: 1 fired, 0 won; a slow primary its backup beats: 1 fired, 1 won.
func TestHedger_RecordsFiredAndWon(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		// primary and backup are how long each attempt takes.
		primary, backup time.Duration

		// fired and won are the expected hedges_total by outcome.
		fired, won float64
	}{
		"fast primary": {primary: 0, backup: 0, fired: 0, won: 0},
		"primary wins": {
			primary: standaloneSlow,
			backup:  standaloneSlower,
			fired:   1,
			won:     0,
		},
		"backup wins": {primary: standaloneSlower, backup: 0, fired: 1, won: 1},
	}

	for name, row := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			harness := newStandalone(t)
			hedger := delayHedger(t, &harness)

			require.NoError(t, hedger.Hedge(context.Background(),
				timedAttempts(row.primary, row.backup)))

			require.InDelta(t, row.fired, harness.value(t,
				"resilience_hedges_total", "outcome", "fired"), 0)
			require.InDelta(t, row.won, harness.value(t,
				"resilience_hedges_total", "outcome", "won"), 0)
		})
	}
}

// timedAttempts returns an operation whose first call takes primary and every later call
// backup, each then succeeding.
func timedAttempts(primary, backup time.Duration) func() error {
	var calls atomic.Int32

	return func() error {
		pause := backup
		if calls.Add(1) == 1 {
			pause = primary
		}

		time.Sleep(pause)

		return nil
	}
}

// delayHedger builds the delay hedger through reswire with the harness's metrics under
// observedName.
func delayHedger(t *testing.T, harness *layerHarness) interfaces.Hedger {
	t.Helper()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, textBaseYaml),
		"resilience:\n  hedge:\n    kind: delay\n    delay: "+
			standaloneHedgeDelay.String()+"\n")

	loader, err := config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err)

	hedger, err := reswire.NewHedge(loader,
		reswire.WithMetrics(prom.NewFromRegistry(harness.registry), observedName))
	require.NoError(t, err)

	return hedger
}

// TestDeadLetterQueue_RecordsSentAndFailed tests that a dead-letter queue handed a
// recorder counts each letter its backend recorded and each one it failed to.
//
// Why this test is important:
//   - a dead letter is a message the system gave up on; a failed send is one it may
//     lose. Both rates are what an operator alerts on.
//
// What it tests:
//   - a send the backend accepts counts sent 1 and returns true; one it fails counts
//     failed 1 and still returns false.
func TestDeadLetterQueue_RecordsSentAndFailed(t *testing.T) {
	t.Parallel()

	harness := newStandalone(t)
	backend := mocks.NewMockDeadLetterBackend(gomock.NewController(t))
	gomock.InOrder(
		backend.EXPECT().Send(gomock.Any(), gomock.Any()).Return(nil),
		backend.EXPECT().Send(gomock.Any(), gomock.Any()).
			Return(apperr.New(apperr.CodeUnavailable, observedDown)),
	)

	queue := deadletter.New(backend, nil).WithRecorder(harness.obs.Recorder)
	letter := fixtures.Build(func(row *interfaces.DeadLetter) { row.ID = observedOp })

	require.True(t, queue.Send(context.Background(), letter))
	require.False(t, queue.Send(context.Background(), letter))

	require.InDelta(t, 1, harness.value(t,
		"resilience_dead_letters_total", "outcome", "sent"), 0)
	require.InDelta(t, 1, harness.value(t,
		"resilience_dead_letters_total", "outcome", "failed"), 0)
}

// TestDedup_RecordsDuplicates tests that an in-memory deduplicator handed a recorder
// counts each duplicate it drops.
//
// Why this test is important:
//   - a rising duplicate rate is redelivery: a consumer slower than its visibility
//     timeout, or a producer retrying; the count is how an operator notices.
//
// What it tests:
//   - a key seen twice counts one duplicate; a key seen again after its TTL lapsed
//     counts none.
func TestDedup_RecordsDuplicates(t *testing.T) {
	t.Parallel()

	harness := newStandalone(t)
	forever := dedup.NewMemory(0).WithRecorder(harness.obs.Recorder)
	brief := dedup.NewMemory(time.Millisecond).WithRecorder(harness.obs.Recorder)

	for _, deduplicator := range []*dedup.Memory{forever, brief} {
		duplicate, err := deduplicator.Seen(context.Background(), observedOp)
		require.NoError(t, err)
		require.False(t, duplicate)
	}

	time.Sleep(standaloneHedgeDelay)

	duplicate, err := forever.Seen(context.Background(), observedOp)
	require.NoError(t, err)
	require.True(t, duplicate)

	duplicate, err = brief.Seen(context.Background(), observedOp)
	require.NoError(t, err)
	require.False(t, duplicate, "the key's TTL lapsed")

	require.InDelta(t, 1, harness.value(t,
		"resilience_duplicates_total", textName, observedName), 0)
}
