package report

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// line ticks each step as it completes, prints one line per finished phase, and ends
// with the failures and a summary.
type line struct {
	// w receives the report.
	w io.Writer

	// verbose shows every step's detail under its tick.
	verbose bool
}

// OnGateStart prints the gate's headline.
func (l *line) OnGateStart(name string, _ []string) {
	_, _ = fmt.Fprintf(l.w, "==> %s\n", name)
}

// OnPhaseStart lists what the phase runs.
func (l *line) OnPhaseStart(_ string, jobs []string) {
	if len(jobs) > 0 {
		_, _ = fmt.Fprintf(l.w, "  running: %s\n", strings.Join(jobs, ", "))
	}
}

// OnStepStart does nothing: a step is reported when it completes.
func (*line) OnStepStart(string, string) {}

// OnStepComplete ticks the step with its duration (and test count), then its
// children.
func (l *line) OnStepComplete(r types.StepResult) {
	_, _ = fmt.Fprintf(l.w, "  %s %s  %s\n", icon(r.Status), r.Name, metric(r))
	if l.verbose {
		indent(l.w, r.Detail)
	}
	if len(r.Children) > 1 {
		for _, c := range r.Children {
			name := strings.TrimPrefix(c.Name, r.Name+"/")
			_, _ = fmt.Fprintf(l.w, "      %s %s  %s\n", icon(c.Status), name, metric(c))
		}
	}
}

// metric is a step's trailer: "(<dur>)", or "N tests · <dur>".
func metric(r types.StepResult) string {
	if r.Tests > 0 {
		return fmt.Sprintf("%d tests · %s", r.Tests, round(r.Duration))
	}
	return fmt.Sprintf("(%s)", round(r.Duration))
}

// OnPhaseComplete prints the phase's aggregate status and elapsed time.
func (l *line) OnPhaseComplete(
	name string,
	results types.StepResults,
	elapsed time.Duration,
) {
	_, _ = fmt.Fprintf(
		l.w,
		"==> %-18s %s  %s\n",
		name,
		aggregate(results),
		round(elapsed),
	)
}

// OnGateComplete prints the failures and the summary.
func (l *line) OnGateComplete(
	_ string,
	results types.StepResults,
	elapsed time.Duration,
) {
	failures(l.w, results)
	_, _ = fmt.Fprintf(l.w, "\n%s\n", summary(results, elapsed))
}

// aggregate reduces a phase's results to one status token of constant width.
func aggregate(results types.StepResults) string {
	counts := results.CountByStatus()
	switch {
	case counts[types.StatusFail] > 0:
		return "FAIL"
	case counts[types.StatusWarn] > 0:
		return "warn"
	case counts[types.StatusPass] > 0:
		return "ok  "
	default:
		return "skip"
	}
}
