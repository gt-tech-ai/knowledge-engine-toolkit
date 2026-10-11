package unit_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/hedge"
)

// TestHedger_Delay_BackupBeatsSlowFirst tests that the delay-then-race hedger returns as
// soon as a fast backup attempt completes, without waiting for a slow first attempt.
//
// Why this test is important:
//   - Hedging exists to collapse tail latency: when the first attempt lands in the slow
//     tail, the backup must be able to win, or the primitive delivers no benefit.
//
// What it tests:
//   - With a short hedge delay, a first attempt that sleeps far longer than the delay is
//     beaten by an immediately-returning backup — Hedge returns well before the slow
//     attempt would.
func TestHedger_Delay_BackupBeatsSlowFirst(t *testing.T) {
	t.Parallel()

	var attempts int32
	op := func() error {
		if atomic.AddInt32(&attempts, 1) == 1 {
			time.Sleep(500 * time.Millisecond) // first attempt lands in the slow tail
			return apperr.Sentinel("slow first attempt")
		}
		return nil // the backup responds immediately
	}

	h, err := hedge.New(hedge.KindDelay, hedge.WithDelay(10*time.Millisecond))
	require.NoError(t, err)

	start := time.Now()
	require.NoError(t, h.Hedge(context.Background(), op), "the fast backup's result wins")
	require.Less(
		t,
		time.Since(start),
		300*time.Millisecond,
		"must not wait for the slow first attempt",
	)
}

// TestHedger_Disabled_RunsOnce tests that the disabled hedger runs the operation exactly
// once, so a non-idempotent path selects KindDisabled and is never double-fired.
//
// Why this test is important:
//   - Hedging a non-idempotent operation would double its side effect; the disabled
//     default is the guard, and it must run the op exactly once.
//
// What it tests:
//   - KindDisabled invokes op a single time and returns its result.
func TestHedger_Disabled_RunsOnce(t *testing.T) {
	t.Parallel()

	var calls int32
	h, err := hedge.New(hedge.KindDisabled)
	require.NoError(t, err)

	require.NoError(t, h.Hedge(context.Background(), func() error {
		atomic.AddInt32(&calls, 1)
		return nil
	}))
	require.Equal(
		t,
		int32(1),
		atomic.LoadInt32(&calls),
		"a disabled hedger must not duplicate the op",
	)
}

// TestHedger_UnknownKind_Errors tests that the factory fails loudly on an unknown kind
// rather than silently returning a nil/degenerate hedger (the factory shape,
// ARCHITECTURE.md#swappable-components).
//
// Why this test is important:
//   - A factory that swallowed an unknown kind would ship a mis-selected resilience
//     policy to production silently; failing loudly surfaces the config error at startup.
//
// What it tests:
//   - New with an out-of-range Kind returns a non-nil error.
func TestHedger_UnknownKind_Errors(t *testing.T) {
	t.Parallel()

	_, err := hedge.New(hedge.Kind(99))
	require.Error(t, err)
}

// TestHedger_NewFromConfig_DefaultsToDisabled tests that the zero/default config builds a
// disabled hedger (hedging is opt-in) and that the Kind names render correctly.
//
// Why this test is important:
//   - Hedging duplicates load, so the default MUST be disabled; a default that hedged
//     would double every read silently. The String names back config parsing and
//     log/metric labels.
//
// What it tests:
//   - NewFromConfig(DefaultConfig()) runs the op exactly once, and the Kind values
//     stringify to "disabled" / "delay".
func TestHedger_NewFromConfig_DefaultsToDisabled(t *testing.T) {
	t.Parallel()

	h, err := hedge.NewFromConfig(hedge.DefaultConfig())
	require.NoError(t, err)

	var calls int32
	require.NoError(t, h.Hedge(context.Background(), func() error {
		atomic.AddInt32(&calls, 1)
		return nil
	}))
	require.Equal(
		t,
		int32(1),
		atomic.LoadInt32(&calls),
		"the default hedger does not hedge",
	)
	require.Equal(t, "disabled", hedge.KindDisabled.String())
	require.Equal(t, "delay", hedge.KindDelay.String())
}

// TestHedger_Delay_RespectsContextCancellation tests that Hedge returns the context error
// when its context is cancelled while the first attempt is still in flight (before the
// backup fires).
//
// Why this test is important:
//   - A hedged read must abort promptly when the caller cancels (deadline, shutdown)
//     instead of waiting out the hedge delay or the in-flight attempt.
//
// What it tests:
//   - With a long delay and an op that blocks, a pre-cancelled context makes Hedge return
//     context.Canceled.
func TestHedger_Delay_RespectsContextCancellation(t *testing.T) {
	t.Parallel()

	h, err := hedge.New(hedge.KindDelay, hedge.WithDelay(time.Hour))
	require.NoError(t, err)

	block := make(chan struct{})
	t.Cleanup(func() { close(block) }) // release the blocked op so its goroutine exits

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = h.Hedge(ctx, func() error {
		<-block
		return nil
	})
	require.ErrorIs(t, err, context.Canceled, "a cancelled context aborts the hedge")
}
