// Package probe runs a set of probes against one target as one service operation: in
// parallel, each under its own deadline, retrying transient failures, with one step per
// probe in probe order.
package probe

import (
	"context"
	"fmt"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/engine"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/service"
)

// Options tunes how a set runs its probes.
type Options struct {
	// Timeout bounds each probe run; zero means no per-probe deadline. A probe still
	// running at its deadline fails with TIMEOUT.
	Timeout time.Duration

	// Parallel bounds how many probes run at once; zero or less means one per CPU.
	Parallel int

	// Retry is how many more times a probe whose failure is transient runs.
	Retry int

	// Strict turns a skipped probe into a failure.
	Strict bool
}

// set is a ProbeSet over a fixed list of probes.
type set[T any] struct {
	// Base runs Run through the service tier as the operation "run".
	service.Base

	// name groups the set's steps.
	name string

	// probes are run in this order.
	probes []interfaces.Probe[T]

	// opts tunes the run.
	opts Options
}

// NewSet returns the ProbeSet named name over probes, decorated from decs as a
// service. A negative Retry or Timeout is CodeInvalidInput.
func NewSet[T any](
	decs decorate.Decorations,
	name string,
	probes []interfaces.Probe[T],
	opts Options,
) (interfaces.ProbeSet[T], error) {
	if opts.Retry < 0 || opts.Timeout < 0 {
		return nil, apperr.New(apperr.CodeInvalidInput,
			"probe set "+name+": retry and timeout must not be negative")
	}
	base, err := service.NewBase[interfaces.ProbeSet[T]](decs, name)
	if err != nil {
		return nil, err
	}
	return &set[T]{Base: base, name: name, probes: probes, opts: opts}, nil
}

// Run runs every probe against target.
func (s *set[T]) Run(ctx context.Context, target T) (types.StepResults, error) {
	return s.Exec(ctx, "run", func(ctx context.Context) (types.StepResults, error) {
		res := engine.Pool(s.opts.Parallel).FanOut(ctx, s.probes,
			func(ctx context.Context, p interfaces.Probe[T]) types.StepResult {
				return s.step(ctx, p, target)
			})
		if err := ctx.Err(); err != nil {
			return res, apperr.Wrap(err, apperr.ContextCode(err, apperr.CodeCanceled),
				"probe set "+s.name)
		}
		return res, nil
	})
}

// step runs one probe, retrying a transient failure, and renders its verdict.
func (s *set[T]) step(
	ctx context.Context,
	p interfaces.Probe[T],
	target T,
) types.StepResult {
	start := time.Now()
	v := s.once(ctx, p, target)
	for try := 0; try < s.opts.Retry && v.Status == types.StatusFail && v.Transient; try++ {
		v = s.once(ctx, p, target)
	}
	r := v.Step(p.Name(), s.name)
	if v.Status == types.StatusSkip && s.opts.Strict {
		r.Status = types.StatusFail
		r.Error = "skipped under strict: " + v.Detail
	}
	r.Duration = time.Since(start)
	return r
}

// once runs p once under the set's timeout. A probe that has not returned by the
// deadline is abandoned (its goroutine ends when the probe returns) and fails with
// TIMEOUT, marked transient. A probe that panics fails.
func (s *set[T]) once(
	ctx context.Context,
	p interfaces.Probe[T],
	target T,
) types.Verdict {
	if s.opts.Timeout == 0 {
		return safeRun(ctx, p, target)
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.Timeout)
	defer cancel()
	done := make(chan types.Verdict, 1)
	go func() { done <- safeRun(ctx, p, target) }()
	select {
	case v := <-done:
		return v
	case <-ctx.Done():
		err := apperr.Wrap(ctx.Err(), apperr.ContextCode(ctx.Err(), apperr.CodeTimeout),
			fmt.Sprintf("probe %s did not finish in %s", p.Name(), s.opts.Timeout))
		return types.Verdict{
			Status:    types.StatusFail,
			Detail:    err.Error(),
			Transient: true,
		}
	}
}

// safeRun runs p, turning a panic into a failed verdict: probes run on worker goroutines,
// where a panic would end the process rather than reach the service's recovery.
func safeRun[T any](
	ctx context.Context,
	p interfaces.Probe[T],
	target T,
) (v types.Verdict) {
	defer func() {
		if r := recover(); r != nil {
			v = types.Verdict{
				Status: types.StatusFail,
				Detail: fmt.Sprintf("probe %s panicked: %v", p.Name(), r),
			}
		}
	}()
	return p.Run(ctx, target)
}
