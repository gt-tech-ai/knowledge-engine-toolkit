package report

import (
	"fmt"
	"io"
	"os"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// summaryErrorWidth caps an error cell in the step summary table.
const summaryErrorWidth = 80

// WriteStepSummary writes a gate's verdict as GitHub step-summary markdown: one line
// when every step passed, otherwise a count heading and a table of the steps that did
// not pass. The heading names the first result's group ("Results" when it has none). A
// write error is CodeInternal.
func WriteStepSummary(w io.Writer, results types.StepResults, total time.Duration) error {
	group := "Results"
	if len(results) > 0 && results[0].Group != "" {
		group = results[0].Group
	}
	n := results.CountByStatus()
	if len(results) > 0 && n[types.StatusPass] == len(results) {
		return writeSummary(
			w,
			"&#x2705; **%s**: All %d checks passed (%s)\n\n",
			group,
			len(results),
			round(total),
		)
	}
	err := writeSummary(
		w,
		"### %s: %d passed, %d failed, %d warned, %d skipped (%s)\n\n"+
			"| Step | Status | Duration | Error |\n|------|--------|----------|-------|\n",
		group,
		n[types.StatusPass],
		n[types.StatusFail],
		n[types.StatusWarn],
		n[types.StatusSkip],
		round(total),
	)
	for _, r := range results {
		if err != nil || r.Status == types.StatusPass {
			continue
		}
		icon := "&#x274C;"
		switch r.Status {
		case types.StatusSkip:
			icon = "&#x23ED;"
		case types.StatusWarn:
			icon = "&#x26A0;"
		case types.StatusPass, types.StatusFail:
		}
		msg := r.Error
		if len(msg) > summaryErrorWidth {
			msg = msg[:summaryErrorWidth-3] + "..."
		}
		err = writeSummary(
			w,
			"| %s | %s | %s | %s |\n",
			r.Name,
			icon,
			round(r.Duration),
			msg,
		)
	}
	if err != nil {
		return err
	}
	return writeSummary(w, "\n")
}

// writeSummary writes one formatted piece of the summary, coding a write error.
func writeSummary(w io.Writer, format string, a ...any) error {
	if _, err := fmt.Fprintf(w, format, a...); err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "report: write step summary")
	}
	return nil
}

// stepSummary appends the gate's verdict to a GitHub step summary file when the gate
// completes. The summary is best-effort: a file that cannot be opened or written is
// skipped, so a reporting failure never fails the run.
type stepSummary struct {
	// path is the step summary file (GITHUB_STEP_SUMMARY).
	path string
}

// OnGateStart does nothing.
func (*stepSummary) OnGateStart(string, []string) {}

// OnPhaseStart does nothing.
func (*stepSummary) OnPhaseStart(string, []string) {}

// OnStepStart does nothing.
func (*stepSummary) OnStepStart(string, string) {}

// OnStepComplete does nothing.
func (*stepSummary) OnStepComplete(types.StepResult) {}

// OnPhaseComplete does nothing.
func (*stepSummary) OnPhaseComplete(string, types.StepResults, time.Duration) {}

// OnGateComplete appends the gate's verdict to the summary file.
func (s *stepSummary) OnGateComplete(
	_ string,
	results types.StepResults,
	elapsed time.Duration,
) {
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_ = WriteStepSummary(f, results, elapsed)
}
