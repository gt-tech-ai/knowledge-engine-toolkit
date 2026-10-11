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
	ctx = WithObserverCtx(ctx, obs)

	var (
		mu    sync.Mutex // guards `all`
		obsMu sync.Mutex // serializes every observer call (incl. FanOut's live emits)
		all   types.StepResults
	)

	g, gctx := errgroup.WithContext(ctx)
	for _, job := range jobs {
		g.Go(func() error {
			// Per-job emitter sharing the group observer mutex: FanOut inside this
			// job delivers its units live (serialized) and counts them.
			emitter := &stepEmitter{mu: &obsMu, obs: obs}
			start := time.Now()
			sr, err := job.Execute(withEmitter(gctx, emitter), runner, root)
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
			// Backstop: stamp the job-execute elapsed onto any result not already
			// timed at a finer granularity (single-op adapter jobs, skip steps, the
			// synthetic error result). FanOut per-unit durations are non-zero and
			// left untouched, preserving multi-unit granularity.
			for i := range sr {
				if sr[i].Duration == 0 {
					sr[i].Duration = elapsed
				}
			}
			delivered := emitter.delivered()
			mu.Lock()
			all = append(all, sr...)
			mu.Unlock()
			// Fire only the steps FanOut did not already deliver live, avoiding a
			// double OnStepComplete. Two shapes occur:
			//   - adapter.Results jobs (lint/typecheck/…): sr IS the FanOut output,
			//     so delivered == len(sr) and nothing more is fired here.
			//   - non-fan-out jobs (delivered == 0): all of sr is fired here.
			//   - a job that appends steps after a fan-out: those trailing extras
			//     (sr[delivered:]) are fired here.
			// adapter.Single/Warn jobs are a fourth case: FanOut delivers N named
			// per-unit results live (e.g. per Go module) but the job returns ONE
			// aggregate ("Go tests"), so delivered (N) >= len(sr) (1) and the
			// aggregate is not re-ticked — it still lands in `all` for the final
			// table and counts. No panic: the guard short-circuits before slicing.
			if delivered < len(sr) {
				obsMu.Lock()
				for _, r := range sr[delivered:] {
					obs.OnStepComplete(r)
				}
				obsMu.Unlock()
			}
			return nil // never cancel siblings; collect all results
		})
	}
	_ = g.Wait()

	return all, nil
}
