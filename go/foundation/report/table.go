package report

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// table prints a result table per phase and for the gate, then the failures.
type table struct {
	// w receives the report.
	w io.Writer

	// verbose shows every step's detail under the gate table.
	verbose bool
}

// OnGateStart does nothing: the table renders per phase.
func (*table) OnGateStart(string, []string) {}

// OnPhaseStart does nothing.
func (*table) OnPhaseStart(string, []string) {}

// OnStepStart does nothing.
func (*table) OnStepStart(string, string) {}

// OnStepComplete does nothing: steps are flushed per phase.
func (*table) OnStepComplete(types.StepResult) {}

// OnPhaseComplete prints the phase's table and summary.
func (t *table) OnPhaseComplete(
	name string,
	results types.StepResults,
	elapsed time.Duration,
) {
	_, _ = fmt.Fprintf(t.w, "\n%s\n", name)
	rows(t.w, results)
	_, _ = fmt.Fprintln(t.w, summary(results, elapsed))
}

// OnGateComplete prints the gate's table, summary and failures.
func (t *table) OnGateComplete(
	name string,
	results types.StepResults,
	elapsed time.Duration,
) {
	_, _ = fmt.Fprintf(t.w, "\n%s - Final Results\n", name)
	rows(t.w, results)
	_, _ = fmt.Fprintln(t.w, summary(results, elapsed))
	if t.verbose {
		for _, r := range results {
			if r.Status != types.StatusFail && len(r.Detail) > 0 {
				_, _ = fmt.Fprintf(t.w, "\n%s %s\n", icon(r.Status), r.Name)
				indent(t.w, r.Detail)
			}
		}
	}
	failures(t.w, results)
}

// rows writes results as an aligned STATUS / STEP / GROUP / TIME / ERROR table.
func rows(w io.Writer, results types.StepResults) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "STATUS\tSTEP\tGROUP\tTIME\tERROR")
	for _, r := range results {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			r.Status, r.Name, r.Group, round(r.Duration), r.Error)
	}
	_ = tw.Flush()
}
