package interfaces

import (
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// ExecutionObserver receives lifecycle events from the execution engine.
// All methods are called synchronously, serialized on a single goroutine — the
// engine owns serialization (RunJobGroup/FanOut funnel concurrent results
// through one goroutine before delivering them here), so implementations need
// not lock for that reason; they still must not block.
//
// Lifecycle order per run: OnGateStart → (per phase: OnPhaseStart → per step:
// OnStepStart? → OnStepComplete) → OnPhaseComplete → … → OnGateComplete. The
// *Start events carry only the names known at that point (phase names at the
// gate, job names at the phase); OnStepStart is best-effort and may be omitted
// by producers that don't know a step's name before it runs.
type ExecutionObserver interface {
	// OnGateStart is called once when the gate begins, before any phase runs,
	// with the names of the phases it will execute.
	OnGateStart(name string, phases []string)

	// OnPhaseStart is called once when a phase begins, before any job runs,
	// with the names of the jobs it will execute.
	OnPhaseStart(name string, jobs []string)

	// OnStepStart is called when an individual step begins, before it runs.
	// Best-effort: producers that don't know a step's name up front may skip it.
	OnStepStart(name, group string)

	// OnStepComplete is called after each individual step finishes.
	OnStepComplete(result types.StepResult)

	// OnPhaseComplete is called once after all jobs in a gate phase finish,
	// with the phase's aggregate results and measured wall-clock elapsed.
	OnPhaseComplete(name string, results types.StepResults, elapsed time.Duration)

	// OnGateComplete is called when the full gate (all phases) finishes, with the
	// gate's aggregate results and measured total wall-clock elapsed.
	OnGateComplete(gateName string, results types.StepResults, elapsed time.Duration)
}
