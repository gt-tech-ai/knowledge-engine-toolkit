package report

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// privateFileMode is the mode of a file only its owner reads.
const privateFileMode = 0o600

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
	if err != nil {
		return err
	}

	err = writeSummaryRows(w, results)
	if err != nil {
		return err
	}

	return writeSummary(w, "\n")
}

// writeSummaryRows writes a row per non-passing result, stopping at the first error.
func writeSummaryRows(w io.Writer, results types.StepResults) error {
	for i := range results {
		r := &results[i]
		if r.Status == types.StatusPass {
			continue
		}

		msg := r.Error
		if len(msg) > summaryErrorWidth {
			msg = msg[:summaryErrorWidth-3] + "..."
		}

		err := writeSummary(
			w,
			"| %s | %s | %s | %s |\n",
			r.Name,
			summaryIcon(r.Status),
			round(r.Duration),
			msg,
		)
		if err != nil {
			return err
		}
	}

	return nil
}

// summaryIcon is the HTML entity for a non-passing status.
func summaryIcon(status types.StepStatus) string {
	if status == types.StatusSkip {
		return "&#x23ED;"
	}

	if status == types.StatusWarn {
		return "&#x26A0;"
	}

	return "&#x274C;"
}

// writeSummary writes one formatted piece of the summary, coding a write error.
func writeSummary(w io.Writer, format string, a ...any) error {
	_, err := fmt.Fprintf(w, format, a...)
	if err != nil {
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
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, privateFileMode)
	if err != nil {
		return
	}

	defer func() { _ = f.Close() }()

	_ = WriteStepSummary(f, results, elapsed)
}
