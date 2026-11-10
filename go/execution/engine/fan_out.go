package engine

import (
	"context"
	"runtime"
	"runtime/debug"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Pool bounds a fan-out's concurrency: Pool(n) runs at most n items at once, and a
// value <= 0 means runtime.NumCPU().
type Pool int

// FanOut runs fn for each item in items with at most p items in flight.
//
// Each invocation is isolated: a panic in fn is recovered and converted to
// StepResult{Status: Fail, Detail: stack} rather than crashing the process. fn
// returning a Fail result is passed through unchanged.
//
// Context cancellation (via gctx derived from ctx) stops items that have not
// yet acquired a worker slot; items already running continue until fn returns.
// All results are collected -- fn always returns nil to the
// errgroup so no unit can cancel its siblings.
func (p Pool) FanOut[T any](
	ctx context.Context,
	items []T,
	callback func(ctx context.Context, item T) types.StepResult,
) types.StepResults {
	numWorkers := int(p)
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

	group, gctx := errgroup.WithContext(ctx)

	// Acquire a worker slot BEFORE spawning each goroutine so at most numWorkers
	// goroutines are ever live at once — a 10k-item fan-out holds ≤numWorkers
	// goroutines, not 10k blocked on the semaphore. On cancellation we stop
	// spawning and mark the remaining items (the tail loop below) as cancelled, so
	// every item still gets a result.
	index := 0

	for ; index < len(items) && acquireSlot(gctx, sem); index++ {
		idx := index

		group.Go(func() error {
			defer func() { <-sem }()

			runUnit(gctx, callback, items[idx], &results[idx], emitter)

			return nil // never cancel sibling goroutines
		})
	}

	// Items not spawned because gctx was cancelled: record a cancel result for
	// each so the returned slice has an entry for every item (matching the
	// pre-acquire-before-spawn shape).
	for ; index < len(items); index++ {
		results[index] = types.StepResult{
			Status:   types.StatusFail,
			Error:    gctx.Err().Error(),
			Name:     "",
			Group:    "",
			Detail:   nil,
			Children: nil,
			Duration: 0,
			Tests:    0,
		}
		emitter.complete(&results[index])
	}

	_ = group.Wait()

	return results
}

// acquireSlot takes a worker slot, or reports false once ctx is cancelled.
func acquireSlot(ctx context.Context, sem chan<- struct{}) bool {
	select {
	case sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// runUnit runs one item, storing its result in slot and delivering it. A panic is
// isolated so one bad item cannot crash the whole run. Wall-clock is measured from the
// moment the unit holds a worker slot (queue-wait excluded), so duration reflects the
// work itself.
func runUnit[T any](
	ctx context.Context,
	callback func(ctx context.Context, item T) types.StepResult,
	item T,
	slot *types.StepResult,
	emitter *stepEmitter,
) {
	start := time.Now()

	defer func() {
		if r := recover(); r != nil {
			*slot = types.StepResult{
				Status:   types.StatusFail,
				Error:    "panic recovered",
				Detail:   debug.Stack(),
				Duration: time.Since(start),
				Name:     "",
				Group:    "",
				Children: nil,
				Tests:    0,
			}
			emitter.complete(slot)
		}
	}()

	stepResult := callback(ctx, item)
	// Stamp the measured duration only when the producer didn't time itself
	// (Duration left zero); a self-timing fn is preserved.
	if stepResult.Duration == 0 {
		stepResult.Duration = time.Since(start)
	}

	*slot = stepResult
	emitter.complete(&stepResult)
}
