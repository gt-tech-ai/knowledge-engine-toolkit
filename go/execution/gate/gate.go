// Package gate composes AnyJobs into a Gate of ordered phases, run with
// stop-on-failure and error aggregation.
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
// New starts one; Parallel, Serial, StopOnFailure and Observe extend it in place
// and return it, so a gate is built as one chain and run with Run or RunLocal.
type Gate struct {
	// obs receives the gate/phase/step lifecycle events as the gate runs.
	obs interfaces.ExecutionObserver

	// name identifies the gate in observer events and the wrapping error.
	name string

	// phases are the ordered phases Run executes.
	phases []Phase

	// stopOnFailure, when true, halts the remaining phases after any phase fails.
	stopOnFailure bool
}

// New returns an empty gate named name that reports to no observer.
func New(name string) *Gate { return &Gate{name: name, obs: engine.NopObserver{}} }

// Parallel adds a phase in which all jobs run concurrently.
func (g *Gate) Parallel(name string, jobs ...interfaces.AnyJob) *Gate {
	g.phases = append(g.phases, Phase{Name: name, Jobs: jobs, parallel: true})
	return g
}

// Serial adds a phase in which jobs run one after another.
func (g *Gate) Serial(name string, jobs ...interfaces.AnyJob) *Gate {
	g.phases = append(g.phases, Phase{Name: name, Jobs: jobs, parallel: false})
	return g
}

// StopOnFailure makes the gate skip its remaining phases after any failure.
func (g *Gate) StopOnFailure() *Gate {
	g.stopOnFailure = true
	return g
}

// Observe reports the gate's lifecycle events to obs; a nil obs keeps the current
// observer, so a caller can pass an optional observer through unchecked.
func (g *Gate) Observe(obs interfaces.ExecutionObserver) *Gate {
	if obs != nil {
		g.obs = obs
	}
	return g
}

// Phases returns the ordered phases of the gate for inspection (e.g. in tests).
func (g *Gate) Phases() []Phase { return g.phases }

// RunLocal runs a gate whose jobs are all runner-less (job.Base.Runnable). It
// supplies a CommandRunner that fails every command, so no phase ever receives a
// nil runner. Use it for in-process gates (e.g. DB seeding) that compose Runnables
// rather than commands.
func (g *Gate) RunLocal(ctx context.Context) error {
	return g.Run(ctx, noRunner{}, "")
}

// Run executes all phases of g in order against runner under root.
// Each phase's jobs run in parallel or serially per configuration.
// Failures are aggregated into a CodeQualityFailed error.
// When StopOnFailure is set, the first failed phase halts subsequent phases.
// Returns nil when all phases pass or produce only warnings.
func (g *Gate) Run(
	ctx context.Context,
	runner interfaces.CommandRunner,
	root string,
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
