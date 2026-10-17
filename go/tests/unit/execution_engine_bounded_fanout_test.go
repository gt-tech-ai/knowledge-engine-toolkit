package unit_test

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/engine"
)

// TestFanOut_HoldsAtMostNumWorkersGoroutines pins the acquire-before-spawn
// fix: a large fan-out must hold at most ~numWorkers live goroutines, not one
// per item.
//
// Why this test is important:
//   - The semaphore always bounded fn *concurrency*; the bug was that every item
//     spawned a goroutine up front that then blocked on the semaphore, so a
//     10k-item fan-out held 10k goroutines. Acquiring the slot before spawning
//     keeps the live goroutine count at O(numWorkers). A probe on
//     runtime.NumGoroutine is the only way to observe the difference. That count is
//     process-wide, so the test runs serially: parallel tests' goroutines would
//     otherwise inflate the peak.
//
// What it tests:
//   - Running FanOut over 10k items with 4 workers, the peak extra goroutines
//     observed from within fn stays far below the item count (a small multiple of
//     numWorkers), where the pre-fix code would show ~10k.
func TestFanOut_HoldsAtMostNumWorkersGoroutines(t *testing.T) {
	const (
		items   = 10000
		workers = 4
	)
	base := runtime.NumGoroutine()
	var peakExtra atomic.Int64

	its := make([]int, items)
	engine.Pool(workers).
		FanOut(context.Background(), its, func(_ context.Context, _ int) types.StepResult {
			extra := int64(runtime.NumGoroutine() - base)
			for {
				cur := peakExtra.Load()
				if extra <= cur || peakExtra.CompareAndSwap(cur, extra) {
					break
				}
			}
			// Hold the worker slot briefly so overlapping workers are observable.
			time.Sleep(time.Microsecond)
			return types.StepResult{Status: types.StatusPass}
		})

	// Pre-fix: ~items extra goroutines. Post-fix: ~workers plus a little runtime
	// slack. 100 is comfortably above numWorkers and far below items.
	require.Less(
		t, peakExtra.Load(), int64(100),
		"FanOut must hold O(numWorkers) goroutines, not O(items)",
	)
}
