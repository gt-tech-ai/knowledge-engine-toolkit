package engine

import (
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// NopObserver is a no-op ExecutionObserver used as a safe default.
type NopObserver struct{}

// Compile-time assertion that NopObserver satisfies the ExecutionObserver contract.
var _ interfaces.ExecutionObserver = NopObserver{}

// OnGateStart discards the gate-start event.
func (NopObserver) OnGateStart(_ string, _ []string) {}

// OnPhaseStart discards the phase-start event.
func (NopObserver) OnPhaseStart(_ string, _ []string) {}

// OnStepStart discards the step-start event.
func (NopObserver) OnStepStart(_, _ string) {}

// OnStepComplete discards the step-complete event.
func (NopObserver) OnStepComplete(_ types.StepResult) {}

// OnPhaseComplete discards the phase-complete event.
func (NopObserver) OnPhaseComplete(_ string, _ types.StepResults, _ time.Duration) {}

// OnGateComplete discards the gate-complete event.
func (NopObserver) OnGateComplete(_ string, _ types.StepResults, _ time.Duration) {}
