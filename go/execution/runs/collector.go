package runs

import (
	"sync"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Collector is the ExecutionObserver an operation runs under when a front-end shapes
// its result afterwards: it records every completed step as reported, a group step
// with its children still nested on it (the group carries its own status, error and
// output; the children are its breakdown). The engine serializes
// observer calls, but a run's steps are read while it is still reporting, so the
// record is mutex-guarded. The zero value is ready to use.
type Collector struct {
	// steps holds each completed top-level step in completion order.
	steps []types.StepResult
	// mu guards steps.
	mu sync.Mutex
}

// OnGateStart is a no-op; the collector records only completed steps.
func (*Collector) OnGateStart(string, []string) {}

// OnPhaseStart is a no-op; the collector records only completed steps.
func (*Collector) OnPhaseStart(string, []string) {}

// OnStepStart is a no-op; the collector records only completed steps.
func (*Collector) OnStepStart(string, string) {}

// OnStepComplete records result, children included.
func (c *Collector) OnStepComplete(result types.StepResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.steps = append(c.steps, result)
}

// OnPhaseComplete is a no-op; its results were already reported step by step.
func (*Collector) OnPhaseComplete(string, types.StepResults, time.Duration) {}

// OnGateComplete is a no-op; its results were already reported step by step.
func (*Collector) OnGateComplete(string, types.StepResults, time.Duration) {}

// Steps returns a copy of the recorded steps.
func (c *Collector) Steps() []types.StepResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]types.StepResult(nil), c.steps...)
}
