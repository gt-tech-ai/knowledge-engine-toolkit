package report

import (
	"encoding/json"
	"io"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// jsonReport writes one JSON document when the gate completes.
type jsonReport struct {
	// w receives the document.
	w io.Writer
}

// jsonStep is one step in the JSON report.
type jsonStep struct {
	// Name is the step's name.
	Name string `json:"name"`

	// Group is the step's group.
	Group string `json:"group,omitempty"`

	// Status is PASS, FAIL, SKIP or WARN.
	Status string `json:"status"`

	// Error is the failure or warning message.
	Error string `json:"error,omitempty"`

	// Detail is the step's captured output.
	Detail string `json:"detail,omitempty"`

	// DurationMS is the step's wall-clock time in milliseconds.
	DurationMS int64 `json:"duration_ms"`

	// Tests is the number of test cases the step ran.
	Tests int `json:"tests,omitempty"`
}

// jsonGate is the JSON report.
type jsonGate struct {
	// Counts maps each status to its step count.
	Counts map[string]int `json:"counts"`

	// Gate is the gate's name.
	Gate string `json:"gate"`

	// Steps are the gate's steps in order.
	Steps []jsonStep `json:"steps"`

	// ElapsedMS is the gate's wall-clock time in milliseconds.
	ElapsedMS int64 `json:"elapsed_ms"`

	// Passed is true when no step failed.
	Passed bool `json:"passed"`
}

// OnGateStart does nothing.
func (*jsonReport) OnGateStart(string, []string) {}

// OnPhaseStart does nothing.
func (*jsonReport) OnPhaseStart(string, []string) {}

// OnStepStart does nothing.
func (*jsonReport) OnStepStart(string, string) {}

// OnStepComplete does nothing: the report is written once.
func (*jsonReport) OnStepComplete(types.StepResult) {}

// OnPhaseComplete does nothing.
func (*jsonReport) OnPhaseComplete(string, types.StepResults, time.Duration) {}

// OnGateComplete writes the report.
func (j *jsonReport) OnGateComplete(
	name string,
	results types.StepResults,
	elapsed time.Duration,
) {
	g := jsonGate{
		Gate: name, ElapsedMS: elapsed.Milliseconds(), Passed: !results.HasFailures(),
		Counts: map[string]int{}, Steps: make([]jsonStep, 0, len(results)),
	}
	for s, n := range results.CountByStatus() {
		g.Counts[s.String()] = n
	}
	for _, r := range results {
		g.Steps = append(g.Steps, jsonStep{
			Name:   r.Name,
			Group:  r.Group,
			Status: r.Status.String(),
			Error:  r.Error,
			Detail: string(
				r.Detail,
			),
			DurationMS: r.Duration.Milliseconds(),
			Tests:      r.Tests,
		})
	}
	enc := json.NewEncoder(j.w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(g)
}
