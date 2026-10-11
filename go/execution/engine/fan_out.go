package engine

import (
	"context"
	"runtime"
	"runtime/debug"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// FanOut runs fn for each item in items with bounded concurrency.
//
// Each invocation is isolated: a panic in fn is recovered and converted to
// StepResult{Status: Fail, Detail: stack} rather than crashing the process. fn
// returning a Fail result is passed through unchanged.
//
// Context cancellation (via gctx derived from ctx) stops items that have not
// yet acquired a worker slot; items already running continue until fn returns.
// All results are collected -- fn always returns nil to the
// errgroup so no unit can cancel its siblings.
//
// numWorkers <= 0 defaults to runtime.NumCPU().
func FanOut[T any](
	ctx context.Context,
	items []T,
	numWorkers int,
	fn func(ctx context.Context, item T) types.StepResult,
) types.StepResults {
	if numWorkers <= 0 {
		numWorkers = runtime.NumCPU()
	}

	results := make([]types.StepResult, len(items))
	sem := make(chan struct{}, numWorkers)

	// Live per-unit delivery (serialized, best-effort) so a long fan-out shows
	// progress instead of going silent. Every unit delivers its result exactly
	// once — normal, panic, or cancel-bail — so the emitter's count equals
	// len(items), letting RunJobGroup fire only the steps appended after the
	// fan-out (no double-fire). nil emitter (no observed run) → no-op.
	emitter := emitterFromCtx(ctx)

	g, gctx := errgroup.WithContext(ctx)

	// Acquire a worker slot BEFORE spawning each goroutine so at most numWorkers
	// goroutines are ever live at once — a 10k-item fan-out holds ≤numWorkers
	// goroutines, not 10k blocked on the semaphore. On cancellation we stop
	// spawning and mark the remaining items (the tail loop below) as cancelled, so
	// every item still gets a result.
	i := 0
spawnLoop:
	for ; i < len(items); i++ {
		select {
		case sem <- struct{}{}:
		case <-gctx.Done():
			break spawnLoop
		}

		idx, it := i, items[i]
		g.Go(func() error {
			defer func() { <-sem }()

			// Measure each unit's wall-clock from the moment it holds a worker
			// slot (queue-wait excluded), so duration reflects the work itself.
			start := time.Now()

			// Isolate panics so one bad item cannot crash the whole run.
			defer func() {
				if r := recover(); r != nil {
					results[idx] = types.StepResult{
						Status:   types.StatusFail,
						Error:    "panic recovered",
						Detail:   debug.Stack(),
						Duration: time.Since(start),
					}
					emitter.complete(results[idx])
				}
			}()

			r := fn(gctx, it)
			// Stamp the measured duration only when the producer didn't time
			// itself (Duration left zero); a self-timing fn is preserved.
			if r.Duration == 0 {
				r.Duration = time.Since(start)
			}
			results[idx] = r
			emitter.complete(r)
			return nil // never cancel sibling goroutines
		})
	}

	// Items not spawned because gctx was cancelled: record a cancel result for
	// each so the returned slice has an entry for every item (matching the
	// pre-acquire-before-spawn shape).
	for ; i < len(items); i++ {
		results[i] = types.StepResult{
			Status: types.StatusFail,
			Error:  gctx.Err().Error(),
		}
		emitter.complete(results[i])
	}

	_ = g.Wait()
	return results
}
