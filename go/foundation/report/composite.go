package report

import (
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// composite forwards every event to each observer in order.
type composite []interfaces.ExecutionObserver

// Composite returns an observer that forwards every event to obs, in order.
func Composite(obs ...interfaces.ExecutionObserver) interfaces.ExecutionObserver {
	return composite(obs)
}

// OnGateStart forwards the event.
func (c composite) OnGateStart(name string, phases []string) {
	for _, o := range c {
		o.OnGateStart(name, phases)
	}
}

// OnPhaseStart forwards the event.
func (c composite) OnPhaseStart(name string, jobs []string) {
	for _, o := range c {
		o.OnPhaseStart(name, jobs)
	}
}

// OnStepStart forwards the event.
func (c composite) OnStepStart(name, group string) {
	for _, o := range c {
		o.OnStepStart(name, group)
	}
}

// OnStepComplete forwards the event.
func (c composite) OnStepComplete(r types.StepResult) {
	for _, o := range c {
		o.OnStepComplete(r)
	}
}

// OnPhaseComplete forwards the event.
func (c composite) OnPhaseComplete(
	name string,
	results types.StepResults,
	d time.Duration,
) {
	for _, o := range c {
		o.OnPhaseComplete(name, results, d)
	}
}

// OnGateComplete forwards the event.
func (c composite) OnGateComplete(
	name string,
	results types.StepResults,
	d time.Duration,
) {
	for _, o := range c {
		o.OnGateComplete(name, results, d)
	}
}
