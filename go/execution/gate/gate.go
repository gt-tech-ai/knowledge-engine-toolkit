// Package gate provides Phase, Gate, GateBuilder, and RunGate for composing
// AnyJobs into ordered phases with stop-on-failure and error aggregation.
package gate

import (
	"context"
	"fmt"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/engine"
)

// GateFactory is a typed producer of a *Gate. CLI workflow types that compose
// a gate implement it so a workflow is a modeled value (parameters as fields)
// rather than a bare builder function. It is a structural contract: callers use
// the concrete workflow type directly; future gate-producing workflows implement
// this interface to signal intent.
//
// interface-composition exemption — execution-engine primitive: a single
// Gate() → *Gate producer contract, not an id-CRUD data-access surface, so it
// embeds no foundation generic.
type GateFactory interface {
	// Gate builds and returns the composed *Gate.
	Gate() *Gate
}

// Phase is a named group of AnyJobs executed either in parallel or serially.
type Phase struct {
	// Name identifies the phase in observer events and failure messages.
	Name string

	// Jobs are the AnyJobs that make up this phase.
	Jobs []interfaces.AnyJob

	// parallel selects the execution mode: true runs all jobs concurrently,
	// false runs them one after another (stopping at the first failure).
	parallel bool
}

// Gate is an ordered sequence of Phases with optional stop-on-failure behaviour.
// Field order: obs (interface) then name (string) before phases (slice) to keep
// pointer-scan range at 40 bytes instead of 48.
type Gate struct {
	// obs receives the gate/phase/step lifecycle events as the gate runs.
	obs interfaces.ExecutionObserver

	// name identifies the gate in observer events and the wrapping error.
	name string

	// phases are the ordered phases executed by RunGate.
	phases []Phase

	// stopOnFailure, when true, halts the remaining phases after any phase fails.
	stopOnFailure bool
}

// GateBuilder assembles a Gate via a fluent API.
type GateBuilder struct {
	// obs is the observer applied to the built gate; defaults to NopObserver
	// when never set via WithObserver.
	obs interfaces.ExecutionObserver

	// name is the gate name carried into the built Gate.
	name string

	// phases accumulate as Parallel/Serial are called, preserving call order.
	phases []Phase

	// stopOnFailure records whether StopOnFailure was requested.
	stopOnFailure bool
}

// NewGate returns a GateBuilder for a named gate.
func NewGate(name string) *GateBuilder { return &GateBuilder{name: name} }

// Parallel adds a phase in which all jobs run concurrently.
func (b *GateBuilder) Parallel(name string, jobs ...interfaces.AnyJob) *GateBuilder {
	b.phases = append(b.phases, Phase{Name: name, Jobs: jobs, parallel: true})
	return b
}

// Serial adds a phase in which jobs run one after another.
func (b *GateBuilder) Serial(name string, jobs ...interfaces.AnyJob) *GateBuilder {
	b.phases = append(b.phases, Phase{Name: name, Jobs: jobs, parallel: false})
	return b
}

// StopOnFailure configures the gate to skip remaining phases after any failure.
func (b *GateBuilder) StopOnFailure() *GateBuilder {
	b.stopOnFailure = true
	return b
}

// WithObserver sets an ExecutionObserver for all phases.
func (b *GateBuilder) WithObserver(obs interfaces.ExecutionObserver) *GateBuilder {
	b.obs = obs
	return b
}

// Build returns the configured Gate.
func (b *GateBuilder) Build() *Gate {
	obs := b.obs
	if obs == nil {
		obs = engine.NopObserver{}
	}
	return &Gate{name: b.name, phases: b.phases, stopOnFailure: b.stopOnFailure, obs: obs}
}

// Phases returns the ordered phases of the gate for inspection (e.g. in tests).
func (g *Gate) Phases() []Phase { return g.phases }

// RunGateNoRunner runs a gate whose jobs are all runner-less (Runnable-backed via
// job.Adapt). It supplies a no-op CommandRunner so no phase ever receives a nil
// runner; the runner is structurally unused by Runnable-backed jobs. Use it for
// in-process gates (e.g. DB seeding) that compose Runnables rather than commands.
func RunGateNoRunner(ctx context.Context, g *Gate) error {
	return RunGate(ctx, noRunner{}, "", g)
}

// RunGate executes all phases of g in order.
// Each phase's jobs run in parallel or serially per configuration.
// Failures are aggregated into a CodeQualityFailed error.
// When StopOnFailure is set, the first failed phase halts subsequent phases.
// Returns nil when all phases pass or produce only warnings.
func RunGate(
	ctx context.Context,
	runner interfaces.CommandRunner,
	root string,
	g *Gate,
) error {
	gateStart := time.Now()

	phaseNames := make([]string, len(g.phases))
	for i, p := range g.phases {
		phaseNames[i] = p.Name
	}
	g.obs.OnGateStart(g.name, phaseNames)

	var (
		all     types.StepResults
		gateErr error
	)

	for _, phase := range g.phases {
		phaseResults, err := runPhase(ctx, runner, root, phase, g.obs)
		all = append(all, phaseResults...)

		if err != nil || phaseResults.HasFailures() {
			phaseErr := phaseFailureError(phase.Name, phaseResults)
			gateErr = apperr.Join(gateErr, phaseErr)
			if g.stopOnFailure {
				break
			}
		}
	}

	g.obs.OnGateComplete(g.name, all, time.Since(gateStart))
	if gateErr != nil {
		return apperr.Wrap(gateErr, apperr.CodeQualityFailed, "gate "+g.name+" failed")
	}
	return nil
}

// runPhase executes all jobs in a phase and fires obs.OnPhaseComplete exactly
// once with the phase's aggregate results and measured wall-clock elapsed —
// for both parallel phases (one RunJobGroup call) and serial phases (one call
// per job). It returns the aggregate and the first execution error, if any.
func runPhase(
	ctx context.Context,
	runner interfaces.CommandRunner,
	root string,
	phase Phase,
	obs interfaces.ExecutionObserver,
) (types.StepResults, error) {
	start := time.Now()

	jobNames := make([]string, len(phase.Jobs))
	for i, j := range phase.Jobs {
		jobNames[i] = j.Meta().Name
	}
	obs.OnPhaseStart(phase.Name, jobNames)

	var (
		results  types.StepResults
		phaseErr error
	)
	if phase.parallel {
		results, phaseErr = engine.RunJobGroup(ctx, runner, root, phase.Jobs, obs)
	} else {
		for _, job := range phase.Jobs {
			sr, err := engine.RunJobGroup(
				ctx,
				runner,
				root,
				[]interfaces.AnyJob{job},
				obs,
			)
			results = append(results, sr...)
			if err != nil {
				phaseErr = err
				break
			}
			if sr.HasFailures() {
				break
			}
		}
	}

	obs.OnPhaseComplete(phase.Name, results, time.Since(start))
	return results, phaseErr
}

// phaseFailureError constructs a CodeQualityFailed error aggregating all
// failed step errors in results.
func phaseFailureError(phaseName string, results types.StepResults) error {
	var errs []error
	for _, r := range results {
		if r.Status == types.StatusFail && r.Error != "" {
			errs = append(
				errs,
				apperr.New(
					apperr.CodeQualityFailed,
					fmt.Sprintf("%s: %s", r.Name, r.Error),
				),
			)
		}
	}
	if len(errs) == 0 {
		return apperr.New(apperr.CodeQualityFailed, "phase "+phaseName+" failed")
	}
	return apperr.Wrap(
		apperr.Join(errs...),
		apperr.CodeQualityFailed,
		"phase "+phaseName+" failed",
	)
}
