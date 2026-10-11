package unit_test

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/loop"
)

// TestLoopConfig_RejectsNegativeDurations tests loop.Config validation.
//
// Why this test is important:
//   - A negative interval or backoff is a configuration bug; accepting it would turn a
//     periodic pass into a hot loop that hammers its backend.
//
// What it tests:
//   - A negative Interval or ErrorBackoff is CodeInvalidInput, from Validate and from
//     NewTicker; zero values are valid.
func TestLoopConfig_RejectsNegativeDurations(t *testing.T) {
	t.Parallel()
	require.NoError(t, loop.Config{}.Validate())
	invalid := []loop.Config{{Interval: -time.Second}, {ErrorBackoff: -time.Second}}
	for _, cfg := range invalid {
		require.Equal(t, apperr.CodeInvalidInput, apperr.Code(cfg.Validate()))
		_, err := loop.NewTicker("bad", loop.Pass(okPass), cfg, quietLogger(t))
		require.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	}
}

// TestTicker_RunsAtStartThenEveryInterval tests the ticker's schedule.
//
// Why this test is important:
//   - A relay or refresher that skipped its startup pass would leave work waiting a full
//     interval after every deploy; one that drifted off its interval would miss its SLO.
//
// What it tests:
//   - With RunAtStart one pass runs at once, then one per Interval; without it the first
//     pass waits one Interval. Run returns nil when its context ends.
func TestTicker_RunsAtStartThenEveryInterval(t *testing.T) {
	t.Parallel()
	for _, atStart := range []bool{true, false} {
		synctest.Test(t, func(t *testing.T) {
			var passes atomic.Int64
			tk, err := loop.NewTicker("refresh", loop.Pass(func(context.Context) error {
				passes.Add(1)
				return nil
			}), loop.Config{Interval: time.Minute, RunAtStart: atStart}, quietLogger(t))
			require.NoError(t, err)

			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- tk.Run(ctx) }()

			synctest.Wait()
			first := int64(0)
			if atStart {
				first = 1
			}
			require.Equal(t, first, passes.Load())
			time.Sleep(3 * time.Minute)
			synctest.Wait()
			require.Equal(t, first+3, passes.Load())

			cancel()
			require.NoError(t, <-done)
		})
	}
}

// TestTicker_FailedPassIsLoggedAndRetried tests the ticker's failure handling.
//
// Why this test is important:
//   - A background loop that died on its first failure would silently stop draining a
//     queue; one that retried without a pause would hammer a backend that is down.
//
// What it tests:
//   - A failed pass is logged at warn with the loop's name and error, the next pass waits
//     ErrorBackoff, and the loop keeps running.
func TestTicker_FailedPassIsLoggedAndRetried(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		boom := apperr.New(apperr.CodeUnavailable, "store down")
		logger := mocks.NewMockLogger(gomock.NewController(t))
		logger.EXPECT().Warn("loop pass failed", "loop", "drain", "error", boom).Times(2)

		var passes atomic.Int64
		tk, err := loop.NewTicker("drain", loop.Pass(func(context.Context) error {
			passes.Add(1)
			return boom
		}), loop.Config{ErrorBackoff: 5 * time.Second}, logger)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- tk.Run(ctx) }()

		synctest.Wait()
		require.Equal(t, int64(1), passes.Load())
		time.Sleep(5 * time.Second)
		synctest.Wait()
		require.Equal(t, int64(2), passes.Load())

		cancel()
		require.NoError(t, <-done)
	})
}

// TestTicker_BackToBackWhenIntervalIsZero tests the drain mode.
//
// Why this test is important:
//   - A stream drainer whose pass already blocks on its source must start the next read
//     as soon as one returns; waiting an interval would add lag to every message.
//
// What it tests:
//   - With a zero Interval, passes run one after another until the context ends, and
//     no goroutine outlives Run (goleak).
func TestTicker_BackToBackWhenIntervalIsZero(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	ctx, cancel := context.WithCancel(t.Context())
	var passes atomic.Int64
	tk, err := loop.NewTicker("drain", loop.Pass(func(context.Context) error {
		if passes.Add(1) == 3 {
			cancel()
		}
		return nil
	}), loop.Config{}, quietLogger(t))
	require.NoError(t, err)
	require.NoError(t, tk.Run(ctx))
	require.Equal(t, int64(3), passes.Load())
}

// TestTicker_ReturnsNilWhenContextEnds tests shutdown during a pass.
//
// Why this test is important:
//   - On drain a pass interrupted by the shutdown is not a failure; logging it would
//     page on every deploy.
//
// What it tests:
//   - A pass that ends with the context's error after cancellation is not logged, and
//     Run returns nil.
//   - A pass that ends with any other error after cancellation is logged at debug only,
//     so a real failure racing the shutdown stays diagnosable.
//   - No goroutine outlives Run (goleak).
func TestTicker_ReturnsNilWhenContextEnds(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	boom := apperr.New(apperr.CodeUnavailable, "flush failed")
	debugged := mocks.NewMockLogger(gomock.NewController(t))
	debugged.EXPECT().
		Debug("loop pass failed during shutdown", "loop", "relay", "error", boom)
	cases := []struct {
		logger *mocks.MockLogger
		result func(ctx context.Context) error
	}{
		{quietLogger(t), func(ctx context.Context) error { return ctx.Err() }},
		{debugged, func(context.Context) error { return boom }},
	}
	for _, tc := range cases {
		started := make(chan struct{})
		tk, err := loop.NewTicker("relay", loop.Pass(func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return tc.result(ctx)
		}), loop.Config{Interval: time.Hour, RunAtStart: true}, tc.logger)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- tk.Run(ctx) }()
		<-started
		cancel()
		require.NoError(t, <-done)
	}
}

// TestGroup_RunsEveryRunnerUntilContextEnds tests the loop group's lifecycle.
//
// Why this test is important:
//   - A service's background loops start and stop together; a loop the group forgot to
//     start never runs, and one it did not wait for outlives the shutdown.
//
// What it tests:
//   - Start runs every runner; Wait returns once the context ends and every runner has
//     returned. Stop on a started group cancels its runners and waits for them.
func TestGroup_RunsEveryRunnerUntilContextEnds(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var a, b atomic.Int64
		g := loop.NewGroup(quietLogger(t))
		g.Add("a", mustTicker(t, &a))
		g.Add("b", mustTicker(t, &b))

		ctx, cancel := context.WithCancel(t.Context())
		require.NoError(t, g.Start(ctx))
		synctest.Wait()
		require.Equal(t, int64(1), a.Load())
		require.Equal(t, int64(1), b.Load())

		cancel()
		g.Wait()
	})

	synctest.Test(t, func(t *testing.T) {
		var a atomic.Int64
		g := loop.NewGroup(quietLogger(t))
		g.Add("a", mustTicker(t, &a))
		require.NoError(t, g.Start(t.Context()))
		synctest.Wait()
		require.NoError(t, g.Stop(t.Context()))
		require.Equal(t, int64(1), a.Load())
	})
}

// TestGroup_OneFailingRunnerDoesNotStopTheOthers tests runner isolation.
//
// Why this test is important:
//   - One loop failing for good (a consumer whose queue was deleted) must not stop the
//     relays beside it, and an operator must see which loop stopped and why.
//
// What it tests:
//   - A runner that returns an error is logged at error with its name; the other runner
//     keeps running until the context ends.
func TestGroup_OneFailingRunnerDoesNotStopTheOthers(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		boom := apperr.New(apperr.CodeNotFound, "queue gone")
		logger := mocks.NewMockLogger(gomock.NewController(t))
		logger.EXPECT().Error("loop stopped", "loop", "consumer", "error", boom).Times(1)

		var ticks atomic.Int64
		g := loop.NewGroup(logger)
		g.Add("consumer", loop.RunnerFunc(func(context.Context) error { return boom }))
		g.Add("relay", mustTicker(t, &ticks))

		ctx, cancel := context.WithCancel(t.Context())
		require.NoError(t, g.Start(ctx))
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		require.Equal(t, int64(3), ticks.Load())

		cancel()
		g.Wait()
	})
}

// TestGroup_ShutdownIsNotAFailure tests how the group logs runners that end on shutdown.
//
// Why this test is important:
//   - A runner interrupted by the shutdown often returns the context's error; logging it
//     at error would page on every deploy.
//
// What it tests:
//   - A runner that returns the context's error after cancellation is not logged.
//   - A runner that returns any other error after cancellation is logged at debug only.
func TestGroup_ShutdownIsNotAFailure(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		boom := apperr.New(apperr.CodeUnavailable, "ack failed")
		logger := mocks.NewMockLogger(gomock.NewController(t))
		logger.EXPECT().
			Debug("loop stopped during shutdown", "loop", "consumer", "error", boom)

		g := loop.NewGroup(logger)
		g.Add("drainer", loop.RunnerFunc(func(ctx context.Context) error {
			<-ctx.Done()
			return apperr.Wrap(ctx.Err(), apperr.CodeCanceled, "ensure group")
		}))
		g.Add("consumer", loop.RunnerFunc(func(ctx context.Context) error {
			<-ctx.Done()
			return boom
		}))
		require.NoError(t, g.Start(t.Context()))
		synctest.Wait()
		require.NoError(t, g.Stop(t.Context()))
	})
}

// TestGroup_MisuseIsReported tests the group's guard rails.
//
// Why this test is important:
//   - A second Start would run every loop twice; a shutdown that waited forever on a
//     stuck loop would blow the pod's termination grace period without saying why.
//
// What it tests:
//   - Stop and Wait on a group that never started return at once; a second Start is
//     CodeConflict; Stop whose context has ended while a loop is still running returns a
//     CodeTimeout error.
func TestGroup_MisuseIsReported(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		idle := loop.NewGroup(quietLogger(t))
		require.NoError(t, idle.Stop(t.Context()))
		idle.Wait()

		release := make(chan struct{})
		g := loop.NewGroup(quietLogger(t))
		g.Add("stuck", loop.RunnerFunc(func(context.Context) error {
			<-release
			return nil
		}))
		require.NoError(t, g.Start(t.Context()))
		require.Equal(t, apperr.CodeConflict, apperr.Code(g.Start(t.Context())))

		stopCtx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		require.Equal(t, apperr.CodeTimeout, apperr.Code(g.Stop(stopCtx)))
		close(release)
		g.Wait()
	})
}

// TestPass_ReturnsTheFunctionsError tests the pass adapter.
//
// Why this test is important:
//   - Pass is how a RunOnce method becomes a workflow the composition root decorates; an
//     adapter that dropped the error would hide every failed pass from the ticker.
//
// What it tests:
//   - Execute returns the wrapped function's error unchanged, and nil on success.
func TestPass_ReturnsTheFunctionsError(t *testing.T) {
	t.Parallel()
	boom := apperr.New(apperr.CodeInternal, "pass failed")
	_, err := loop.Pass(func(context.Context) error { return boom }).
		Execute(t.Context(), struct{}{})
	require.ErrorIs(t, err, boom)
	_, err = loop.Pass(okPass).Execute(t.Context(), struct{}{})
	require.NoError(t, err)
}

// okPass is a pass that always succeeds.
func okPass(context.Context) error { return nil }

// quietLogger returns a mock logger that fails the test on any warn or error.
func quietLogger(t *testing.T) *mocks.MockLogger {
	t.Helper()
	return mocks.NewMockLogger(gomock.NewController(t))
}

// mustTicker returns a one-minute ticker with a startup pass that counts into n.
func mustTicker(t *testing.T, n *atomic.Int64) *loop.Ticker {
	t.Helper()
	tk, err := loop.NewTicker("count", loop.Pass(func(context.Context) error {
		n.Add(1)
		return nil
	}), loop.Config{Interval: time.Minute, RunAtStart: true}, quietLogger(t))
	require.NoError(t, err)
	return tk
}
