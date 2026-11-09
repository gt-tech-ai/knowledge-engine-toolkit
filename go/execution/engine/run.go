package engine

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// RunJobGroup executes jobs concurrently and collects all StepResults. Each
// job's results are forwarded to obs.OnStepComplete as they arrive. Phase-level
// aggregation (obs.OnPhaseComplete) is the caller's responsibility (gate.runPhase),
// because a serial phase invokes RunJobGroup once per job and must fire
// OnPhaseComplete exactly once for the whole phase. Context cancellation stops
// jobs that have not yet acquired a worker slot; in-flight jobs run to completion.
func RunJobGroup(
	ctx context.Context,
	runner interfaces.CommandRunner,
	root string,
	jobs []interfaces.AnyJob,
	obs interfaces.ExecutionObserver,
) (types.StepResults, error) {
	group := &jobGroup{obs: obs}

	g, gctx := errgroup.WithContext(ctx)
	for _, job := range jobs {
		g.Go(func() error {
			group.run(gctx, runner, root, job)

			return nil // never cancel siblings; collect all results
		})
	}

	_ = g.Wait()

	return group.all, nil
}

// jobGroup is the shared state of one RunJobGroup call.
type jobGroup struct {
	// obs receives every step completion.
	obs interfaces.ExecutionObserver

	// all collects the results of every job.
	all types.StepResults

	// mu guards all.
	mu sync.Mutex

	// obsMu serializes every observer call, including FanOut's live emits.
	obsMu sync.Mutex
}

// run executes one job and records its results.
func (g *jobGroup) run(
	ctx context.Context,
	runner interfaces.CommandRunner,
	root string,
	job interfaces.AnyJob,
) {
	// Per-job emitter sharing the group observer mutex: FanOut inside this
	// job delivers its units live (serialized) and counts them.
	emitter := &stepEmitter{mu: &g.obsMu, obs: g.obs}
	start := time.Now()
	sr, err := job.Execute(withEmitter(ctx, emitter), runner, root)
	elapsed := time.Since(start)

	if err != nil {
		// Discoverer or pre-execution failure: surface as a single Fail result
		// so the gate can detect the failure rather than seeing zero results.
		sr = types.StepResults{{
			Name:   job.Meta().Name,
			Status: types.StatusFail,
			Error:  err.Error(),
		}}
	}

	stampElapsed(sr, elapsed)

	delivered := emitter.delivered()

	g.collect(sr)
	g.fireUndelivered(sr, delivered)
}

// stampElapsed is the backstop that stamps the job-execute elapsed onto any result not
// already timed at a finer granularity (single-op adapter jobs, skip steps, the
// synthetic error result). FanOut per-unit durations are non-zero and left untouched,
// preserving multi-unit granularity.
func stampElapsed(sr types.StepResults, elapsed time.Duration) {
	for i := range sr {
		if sr[i].Duration == 0 {
			sr[i].Duration = elapsed
		}
	}
}

// collect appends a job's results to the group's.
func (g *jobGroup) collect(sr types.StepResults) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.all = append(g.all, sr...)
}

// fireUndelivered fires only the steps FanOut did not already deliver live, avoiding a
// double OnStepComplete. Two shapes occur:
//   - adapter.Results jobs (lint/typecheck/…): sr IS the FanOut output,
//     so delivered == len(sr) and nothing more is fired here.
//   - non-fan-out jobs (delivered == 0): all of sr is fired here.
//   - a job that appends steps after a fan-out: those trailing extras
//     (sr[delivered:]) are fired here.
//
// adapter.Single/Warn jobs are a fourth case: FanOut delivers N named
// per-unit results live (e.g. per Go module) but the job returns ONE
// aggregate ("Go tests"), so delivered (N) >= len(sr) (1) and the
// aggregate is not re-ticked — it still lands in `all` for the final
// table and counts. No panic: the guard short-circuits before slicing.
func (g *jobGroup) fireUndelivered(sr types.StepResults, delivered int) {
	if delivered >= len(sr) {
		return
	}

	g.obsMu.Lock()
	defer g.obsMu.Unlock()

	for _, r := range sr[delivered:] {
		g.obs.OnStepComplete(r)
	}
}
