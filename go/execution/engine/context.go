package engine

import (
	"context"
	"sync"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// stepEmitter delivers per-step observer events live, serialized. FanOut runs
// units on many goroutines and the observer contract forbids overlapping calls,
// so every delivery goes through mu (shared across all jobs in a group, so even
// parallel fan-outs never overlap). count (guarded by mu) records how many steps
// FanOut delivered for the current job, so RunJobGroup can fire only the steps
// FanOut did not — the extras a service appends after the fan-out, or all of a
// non-fan-out job's results — without double-firing the fan-out units.
type stepEmitter struct {
	// mu serializes every observer delivery; it is shared across all jobs in a
	// group so even parallel fan-outs never overlap an OnStepComplete call.
	mu *sync.Mutex

	// obs is the observer that live per-step results are delivered to.
	obs interfaces.ExecutionObserver

	// count records how many steps were delivered live for the current job
	// (guarded by mu), so RunJobGroup can fire only the steps FanOut did not.
	count int
}

// complete delivers r live (serialized) and counts it. Nil-safe.
func (e *stepEmitter) complete(r types.StepResult) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.obs.OnStepComplete(r)
	e.count++
	e.mu.Unlock()
}

// delivered returns how many steps were delivered live. Nil-safe.
func (e *stepEmitter) delivered() int {
	if e == nil {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.count
}

// emitterKey is the private context key under which the per-job stepEmitter is
// stored, distinct from observerKey so the two never collide.
type emitterKey struct{}

// withEmitter returns a derived context carrying e (per-job).
func withEmitter(ctx context.Context, e *stepEmitter) context.Context {
	return context.WithValue(ctx, emitterKey{}, e)
}

// emitterFromCtx extracts the per-job stepEmitter, or nil when none is present.
func emitterFromCtx(ctx context.Context) *stepEmitter {
	if e, ok := ctx.Value(emitterKey{}).(*stepEmitter); ok {
		return e
	}
	return nil
}
