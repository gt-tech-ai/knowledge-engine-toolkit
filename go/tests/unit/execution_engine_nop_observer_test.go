package unit_test

import (
	"testing"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/engine"
)

// TestNopObserver_AllEventsAreInert tests that the no-op ExecutionObserver accepts
// every lifecycle event without panicking.
//
// Why this test is important:
//   - NopObserver is the safe default a gate uses when no observer is wired; a panic
//     in any callback would crash every gate run that opted out of observation.
//
// What it tests:
//   - Each of the six observer callbacks can be invoked and returns without panicking.
func TestNopObserver_AllEventsAreInert(t *testing.T) {
	t.Parallel()

	var o engine.NopObserver
	o.OnGateStart("gate", []string{"phase"})
	o.OnPhaseStart("phase", []string{"step"})
	o.OnStepStart("phase", "step")
	o.OnStepComplete(types.StepResult{})
	o.OnPhaseComplete("phase", types.StepResults{}, time.Second)
	o.OnGateComplete("gate", types.StepResults{}, time.Second)
}
