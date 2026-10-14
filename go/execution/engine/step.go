// Package engine provides execution infrastructure: bounded concurrent fan-out,
// observer dispatch, job-group execution, and simple function steps.
package engine

import (
	"context"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Step is a named function run as one unit of work, for commands that need
// function-to-result wrapping without a job's runner and root.
type Step struct {
	// Run is the work; a non-nil error fails the step.
	Run func(context.Context) error

	// Name identifies the step in its result.
	Name string

	// Group categorizes the step for display.
	Group string

	// WarnOnly downgrades a failure to a warning.
	WarnOnly bool
}

// Execute runs the step and returns its timed result. An uncoded error is wrapped
// as INTERNAL so the result always carries a code.
func (s Step) Execute(ctx context.Context) types.StepResult {
	start := time.Now()
	err := s.Run(ctx)
	result := types.StepResult{
		Name:     s.Name,
		Group:    s.Group,
		Status:   types.StatusPass,
		Duration: time.Since(start),
	}
	if err == nil {
		return result
	}
	var appErr *apperr.AppError
	if !apperr.As(err, &appErr) {
		err = apperr.Wrap(err, apperr.CodeInternal, s.Name+" failed")
	}
	result.Status = types.StatusFail
	if s.WarnOnly {
		result.Status = types.StatusWarn
	}
	result.Error = err.Error()
	return result
}

// Steps is an ordered list of steps.
type Steps []Step

// Serial runs every step in order and returns their results; a failure does not
// stop the steps after it.
func (s Steps) Serial(ctx context.Context) types.StepResults {
	if len(s) == 0 {
		return nil
	}
	results := make(types.StepResults, 0, len(s))
	for _, step := range s {
		results = append(results, step.Execute(ctx))
	}
	return results
}
