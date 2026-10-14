package unit_test

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/reswire"
)

// assertBulkheadLimit fills the bulkhead to `limit` concurrent held slots (each blocked
// in Execute) and asserts the next TryExecute is rejected — a race-free proof of the
// effective concurrency ceiling. It releases the held slots before returning.
func assertBulkheadLimit(t *testing.T, bh interfaces.Bulkhead, limit int) {
	t.Helper()
	entered := make(chan struct{}, limit)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for range limit {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = bh.Execute(context.Background(), func() error {
				entered <- struct{}{}
				<-release
				return nil
			})
		}()
	}
	for range limit { // all `limit` slots now held
		<-entered
	}
	require.Error(t, bh.TryExecute(func() error { return nil }),
		"an over-limit TryExecute must be rejected (bulkhead full)")
	close(release)
	wg.Wait()
}

// TestReswire_HedgeDefault_RunsOnce tests that NewHedge builds the behavior-preserving
// disabled hedger when no resilience.hedge overlay is present — the op runs exactly once,
// no backup.
//
// Why this test is important:
//   - Hedging duplicates load and is only safe for idempotent paths; the default MUST be
//     disabled
//
// (default-preservation), or wiring a hedger would silently double backend load.
//
// What it tests:
//   - With no overlay, the hedger runs the op exactly once.
func TestReswire_HedgeDefault_RunsOnce(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), "database:\n  host: localhost\n")
	loader, err := config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err)

	h, err := reswire.NewHedge(loader)
	require.NoError(t, err)

	var calls int32
	require.NoError(t, h.Hedge(context.Background(), func() error {
		atomic.AddInt32(&calls, 1)
		return nil
	}))
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls),
		"the disabled default hedger runs the op exactly once (no backup)")
}

// TestReswire_HedgeReadsOverlay_FiresBackup tests that a resilience.hedge overlay
// selecting the delay kind is READ and applied — a held first attempt triggers a backup
// (the op runs twice).
//
// Why this test is important:
//   - requires the hedge knob be overlay-tunable. The observable difference between the
//     disabled default (1 op) and the configured delay kind (2 ops) proves the config
//     selects the hedger, not a frozen default.
//
// What it tests:
//   - `resilience.hedge: {kind: delay, delay: 1ms}` yields a hedger that fires a backup
//     when the first attempt is slow (the op is invoked a second time).
func TestReswire_HedgeReadsOverlay_FiresBackup(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"),
		"resilience:\n  hedge:\n    kind: delay\n    delay: 1ms\n")
	loader, err := config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err)

	h, err := reswire.NewHedge(loader)
	require.NoError(t, err)

	var calls int32
	first := make(chan struct{})
	backup := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- h.Hedge(context.Background(), func() error {
			if atomic.AddInt32(&calls, 1) == 1 {
				close(first)
				<-release // hold the first attempt so the delay elapses and the backup fires
				return nil
			}
			close(backup)
			return nil
		})
	}()

	<-first // first attempt running
	// backup fired — proves the delay kind was read (blocks forever if it never fires)
	<-backup
	require.NoError(t, <-done)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls),
		"the configured delay hedger fires a backup attempt")
	close(release)
}

// TestReswire_AdaptiveThrottleDefault_NeverSheds tests that NewAdaptiveThrottle builds
// the behavior-preserving disabled (no-op) throttler when no overlay is present — every
// op runs, and the rejection probability stays 0 even under sustained failure.
//
// Why this test is important:
//   - Load shedding is opt-in; enabling it blindly could shed legitimate traffic. The
//     default MUST
//
// be a pass-through (default-preservation).
//
// What it tests:
//   - With no overlay, the throttler runs every op and never sheds (RejectionProbability
//     == 0).
func TestReswire_AdaptiveThrottleDefault_NeverSheds(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), "database:\n  host: localhost\n")
	loader, err := config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err)

	th, err := reswire.NewAdaptiveThrottle(loader)
	require.NoError(t, err)

	boom := apperr.Sentinel("boom")
	calls := 0
	for range 100 {
		_ = th.Do(context.Background(), func() error { calls++; return boom })
	}
	assert.Equal(
		t,
		100,
		calls,
		"the disabled throttler runs every op (never sheds), even under sustained failure",
	)
}

// TestReswire_AdaptiveThrottleReadsOverlay_Sheds tests that a
// resilience.adaptive_throttle overlay selecting the enabled kind is READ and applied —
// sustained failures raise the rejection probability.
//
// Why this test is important:
//   - requires the throttle knob be overlay-tunable. The observable difference between
//     the disabled default (rejection stays 0) and the enabled kind (rejection rises
//     under failure) proves the config selects the throttler, not a frozen default.
//
// What it tests:
//   - `resilience.adaptive_throttle: {kind: enabled}` yields a throttler that sheds a
//     growing share of requests under sustained backend failure — some ops are shed
//     (never invoked), so the op-run count falls below the attempt count. (The default
//     disabled throttler runs all N; the enabled one runs < N — the observable difference
//     proving the config selects the throttler. The SRE rejection probability rises
//     toward ~1 under sustained failure, so shedding is effectively certain here.)
func TestReswire_AdaptiveThrottleReadsOverlay_Sheds(t *testing.T) {
	dir := t.TempDir()
	writeFile(
		t,
		filepath.Join(dir, "base.yaml"),
		"resilience:\n  adaptive_throttle:\n    kind: enabled\n    k: 2.0\n    decay: 0.98\n",
	)
	loader, err := config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err)

	th, err := reswire.NewAdaptiveThrottle(loader)
	require.NoError(t, err)

	boom := apperr.Sentinel("boom")
	const attempts = 100
	calls := 0
	for range attempts {
		_ = th.Do(context.Background(), func() error { calls++; return boom })
	}
	assert.Less(
		t,
		calls,
		attempts,
		"the enabled throttler sheds some ops under sustained failure (op-run count < "+
			"attempts)",
	)
}
