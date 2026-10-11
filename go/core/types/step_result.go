package types

import "time"

// StepStatus represents the outcome of a step execution.
type StepStatus int

const (
	// StatusPass indicates successful execution.
	StatusPass StepStatus = iota
	// StatusFail indicates execution failure.
	StatusFail
	// StatusSkip indicates the step was skipped (condition not met).
	StatusSkip
	// StatusWarn indicates execution completed with warnings (non-blocking).
	StatusWarn
)

// String returns the human-readable status name.
func (s StepStatus) String() string {
	switch s {
	case StatusPass:
		return "PASS"
	case StatusFail:
		return "FAIL"
	case StatusSkip:
		return "SKIP"
	case StatusWarn:
		return "WARN"
	default:
		return "UNKNOWN"
	}
}

// StepResult represents the outcome of a single step execution.
type StepResult struct {
	// Name is the human-readable step name displayed in result tables.
	Name string

	// Group is the phase or category label used to group steps in result tables.
	Group string

	// Error is the human-readable error message; empty when the step passed.
	Error string

	// Detail holds captured command output (stdout+stderr) for failure diagnostics.
	Detail []byte

	// Children holds sub-results of a composite step — e.g. the per-package
	// breakdown of a Go module's test run — which observers render as an indented
	// list under the step. Nil for a leaf step; children are not counted in the
	// top-level status/summary tallies.
	Children StepResults

	// Status is the execution outcome.
	Status StepStatus

	// Duration is the wall-clock time taken to execute the step.
	Duration time.Duration

	// Tests is the number of individual test cases the step ran (Go test
	// functions, pytest items). Zero when the step is not a test run or the count
	// is unknown; observers render it as an "N tests" annotation.
	Tests int
}

// StepResults is an ordered slice of StepResult values with query helpers.
type StepResults []StepResult

// HasFailures returns true if any result has Status == StatusFail.
func (rs StepResults) HasFailures() bool {
	for _, r := range rs {
		if r.Status == StatusFail {
			return true
		}
	}
	return false
}

// AllPassed returns true if all results have Status == StatusPass.
// An empty slice is considered passed.
func (rs StepResults) AllPassed() bool {
	for _, r := range rs {
		if r.Status != StatusPass {
			return false
		}
	}
	return true
}

// CountByStatus returns a map of status -> count for summary displays.
func (rs StepResults) CountByStatus() map[StepStatus]int {
	counts := make(map[StepStatus]int)
	for _, r := range rs {
		counts[r.Status]++
	}
	return counts
}

// FailedNames returns the names of all failed steps.
func (rs StepResults) FailedNames() []string {
	var names []string
	for _, r := range rs {
		if r.Status == StatusFail {
			names = append(names, r.Name)
		}
	}
	return names
}
