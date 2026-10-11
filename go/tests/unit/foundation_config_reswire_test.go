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

// TestReswire_BreakerReadsConfig_NotFrozenDefault tests that NewBreaker honors an
// overridden resilience.circuit_breaker threshold instead of the frozen default.
//
// Why this test is important:
//   - This is the point of: the repo/client circuit breakers were
//     frozen at circuitbreaker.DefaultConfig() (consecutive_failures=5). If the
//     provider still ignored config, an operator tightening the breaker would have
//     no effect. The overridden threshold (1) trips after failures a default
//     (5) breaker would not — proving the config, not the default, is in force.
//
// What it tests:
//   - With consecutive_failures overridden to 1, the breaker opens after two
//     failures (rejecting the next call without invoking it) — behavior the
//     default-5 breaker would not exhibit.
func TestReswire_BreakerReadsConfig_NotFrozenDefault(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), `
resilience:
  circuit_breaker:
    consecutive_failures: 1
    max_requests: 1
    interval: 60s
    timeout: 30s
    failure_ratio: 0.5
    min_requests: 10
`)
	loader, err := config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err)

	cb, err := reswire.NewBreaker(loader, "test.breaker")
	require.NoError(t, err)

	boom := apperr.Sentinel("boom")
	_ = cb.Execute(func() error { return boom })
	_ = cb.Execute(func() error { return boom })

	// With consecutive_failures=1 the breaker is now open; a default-5 breaker
	// would still be closed after two failures.
	called := false
	err = cb.Execute(func() error { called = true; return nil })
	require.Error(t, err, "overridden breaker must be open after failures")
	assert.False(t, called, "an open breaker must not invoke the function")
}

// TestReswire_RetrierDefault_Builds tests that NewRetrier builds a working
// retrier from the default (no-overlay) config.
//
// Why this test is important:
//   - The retry provider replaced retry.New(KindExponential); an absent
//     resilience.retry section must still produce a retrier with the
//     behavior-preserving defaults (not error, not a no-op).
//
// What it tests:
//   - With no resilience.retry overlay, NewRetrier returns a retrier that retries
//     a transiently-failing op to eventual success.
func TestReswire_RetrierDefault_Builds(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), "database:\n  host: localhost\n")
	loader, err := config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err)

	r, err := reswire.NewRetrier(loader)
	require.NoError(t, err)

	attempts := 0
	err = r.Retry(context.Background(), func() error {
		attempts++
		if attempts < 2 {
			return apperr.Sentinel("transient")
		}
		return nil
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, attempts, 2, "retrier must retry a transient failure")
}

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

// TestReswire_BulkheadReadsOverlay tests that NewBulkhead honors a
// resilience.adaptive_limit overlay over the consumer's fallback default.
//
// Why this test is important:
//   - The internal-service admission bulkhead was once a hardcoded const; it must be
//     overlay-tunable per environment. This proves the overlay's max_concurrent (1) wins
//     over the caller's fallback (5), so a deployment can tighten/loosen admission by
//     config.
//
// What it tests:
//   - `resilience.adaptive_limit.max_concurrent: 1` yields a bulkhead that admits exactly
//     1 concurrent op (the overlay, not the 5 fallback, is in force).
func TestReswire_BulkheadReadsOverlay(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), `
resilience:
  adaptive_limit:
    kind: channel
    max_concurrent: 1
`)
	loader, err := config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err)

	bh, err := reswire.NewBulkhead(loader, 5) // caller fallback 5; overlay's 1 must win
	require.NoError(t, err)
	assertBulkheadLimit(t, bh, 1)
}

// TestReswire_BulkheadDefault_WhenAbsent tests that NewBulkhead uses the caller's
// fallback default when no resilience.adaptive_limit overlay is present — the parity
// guarantee.
//
// Why this test is important:
//   - The two internal-bulkhead call sites pass DefaultInternalMaxConcurrent (128) as the
//     fallback; an absent overlay MUST preserve that behavior (not silently drop to the
//     schema's generic default), or the reconciliation would change admission behavior.
//
// What it tests:
//   - With no adaptive_limit section, a fallback of 1 yields a bulkhead admitting exactly
//     1 concurrent op (the caller default is applied).
func TestReswire_BulkheadDefault_WhenAbsent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), "database:\n  host: localhost\n")
	loader, err := config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err)

	bh, err := reswire.NewBulkhead(loader, 1) // no overlay → caller fallback (1) applies
	require.NoError(t, err)
	assertBulkheadLimit(t, bh, 1)
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

// TestReswire_RetrierReadsConfig_MaxTries pins that a configured
// resilience.retry.max_retries value changes retry behavior — the coverage gap the recon
// found (retry had only default-builds + error-path tests, no positive-value overlay
// test, unlike the breaker's TestReswire_BreakerReadsConfig).
//
// Why this test is important:
//   - The retry budget is the point of 's config wiring; if the provider ignored the
//     value, an operator tightening/loosening retries would have no effect. The
//     configured max (2) attempts a persistently-failing op exactly twice — a default (3)
//     retrier would attempt it a third time.
//
// What it tests:
//   - `resilience.retry.max_retries: 2` (tiny intervals) yields a retrier that invokes an
//     always-failing op exactly 2 times before giving up.
func TestReswire_RetrierReadsConfig_MaxTries(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.yaml"), `
resilience:
  retry:
    max_retries: 2
    initial_interval: 1ms
    max_interval: 1ms
    multiplier: 1.0
    max_elapsed_time: 5s
`)
	loader, err := config.New(config.KindViper, config.WithBaseDir(dir))
	require.NoError(t, err)

	r, err := reswire.NewRetrier(loader)
	require.NoError(t, err)

	attempts := 0
	err = r.Retry(context.Background(), func() error {
		attempts++
		return apperr.Sentinel("always fails")
	})
	require.Error(t, err, "a persistently-failing op eventually gives up")
	assert.Equal(
		t,
		2,
		attempts,
		"max_retries:2 caps the op at 2 attempts (a default-3 retrier would attempt a 3rd)",
	)
}
