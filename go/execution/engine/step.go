// Package engine provides execution infrastructure: concurrent fan-out,
// observer dispatch, step execution, and context-based observer propagation.
package engine

import (
	"context"
	"runtime"
	"sync"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Step represents a single executable unit returning a StepResult.
// Used by CLI commands that need simple function-to-result wrapping
// without the full Job discover/map/execute lifecycle.
//
// interface-composition exemption — execution-engine primitive: a single
// Execute(ctx) → StepResult unit of work, not an id-CRUD data-access surface, so it
// embeds no foundation generic.
type Step interface {
	// Execute runs the step and returns its result.
	Execute(ctx context.Context) types.StepResult
}

// StepFunc is a function that can be wrapped as a Step.
type StepFunc func(context.Context) error

// StepOption is a functional option for configuring a Step.
type StepOption func(*stepConfig)

// stepConfig holds step configuration applied via StepOption.
type stepConfig struct {
	// WarnOnly downgrades failures to warnings when true.
	WarnOnly bool
}

// StepWarnOnly returns a StepOption that downgrades failures to warnings.
func StepWarnOnly() StepOption {
	return func(c *stepConfig) { c.WarnOnly = true }
}

// NewStep creates a Step from a function with optional configuration.
func NewStep(
	name, group string,
	fn StepFunc,
	opts ...StepOption,
) Step {
	cfg := &stepConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	base := &funcStep{name: name, group: group, fn: fn}
	if cfg.WarnOnly {
		return &warnOnlyStep{inner: base}
	}
	return base
}

// funcStep wraps a StepFunc as a Step with timing and error handling.
type funcStep struct {
	// fn is the step function to execute.
	fn StepFunc
	// name identifies this step in results.
	name string
	// group categorizes this step for display grouping.
	group string
}

// StepName returns the step name for identification.
func (s *funcStep) StepName() string { return s.name }

// StepGroup returns the step group for display categorization.
func (s *funcStep) StepGroup() string { return s.group }

// Execute runs the function and returns a StepResult with timing.
func (s *funcStep) Execute(ctx context.Context) types.StepResult {
	start := time.Now()
	err := s.fn(ctx)

	result := types.StepResult{
		Name:     s.name,
		Group:    s.group,
		Status:   types.StatusPass,
		Duration: time.Since(start),
	}

	if err != nil {
		result.Status = types.StatusFail

		var appErr *apperr.AppError
		if !apperr.As(err, &appErr) {
			err = apperr.Wrap(err, apperr.CodeInternal, s.name+" failed")
		}
		result.Error = err.Error()
	}

	return result
}

// warnOnlyStep downgrades StatusFail to StatusWarn.
type warnOnlyStep struct {
	// inner is the wrapped step whose failures are downgraded. It is always the
	// *funcStep NewStep built, so name/group delegate to it directly.
	inner *funcStep
}

// Execute runs the inner step and downgrades failures to warnings.
func (d *warnOnlyStep) Execute(ctx context.Context) types.StepResult {
	result := d.inner.Execute(ctx)
	if result.Status == types.StatusFail {
		result.Status = types.StatusWarn
	}
	return result
}

// StepName returns the name of the inner step.
func (d *warnOnlyStep) StepName() string { return d.inner.StepName() }

// StepGroup returns the group of the inner step.
func (d *warnOnlyStep) StepGroup() string { return d.inner.StepGroup() }

// RunStepsSerial executes steps one at a time in order, returning one result per
// step. It is the serial counterpart of RunStepsParallel for order-dependent step
// sequences — e.g. a resource prune where compose-down must precede image removal.
func RunStepsSerial(ctx context.Context, steps ...Step) types.StepResults {
	if len(steps) == 0 {
		return nil
	}
	results := make(types.StepResults, 0, len(steps))
	for _, s := range steps {
		results = append(results, s.Execute(ctx))
	}
	return results
}

// RunStepsParallel executes steps concurrently and returns results indexed to
// match input order. Concurrency is bounded at runtime.NumCPU().
func RunStepsParallel(ctx context.Context, steps ...Step) types.StepResults {
	if len(steps) == 0 {
		return nil
	}

	results := make(types.StepResults, len(steps))
	limit := runtime.NumCPU()
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup

	for i, step := range steps {
		// Acquire a worker slot BEFORE spawning so at most `limit` goroutines are
		// ever live, rather than spawning len(steps) goroutines that all block on
		// the semaphore.
		sem <- struct{}{}
		wg.Add(1)
		go func(idx int, s Step) {
			defer wg.Done()
			defer func() { <-sem }()
			results[idx] = s.Execute(ctx)
		}(i, step)
	}

	wg.Wait()
	return results
}
