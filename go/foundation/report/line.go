package report

import (
	"fmt"
	"strings"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// phaseLabelWidth pads a phase name so the phase lines' status column aligns.
const phaseLabelWidth = 18

// line reports a gate one line per phase, then the failure block and a one-line
// summary. Its interactive layout adds the gate and phase headers, a tick per step
// (with child steps and test counts) and a table of any step that never ticked.
type line struct {
	// p is the shared printer.
	p *printer

	// ticked records the steps already printed as ticks, so the gate's closing table
	// shows only the rest.
	ticked map[string]bool
}

// newLine returns a line reporter over p.
func newLine(p *printer) *line { return &line{p: p, ticked: map[string]bool{}} }

// OnGateStart prints the bold gate header (interactive only).
func (l *line) OnGateStart(name string, _ []string) {
	if l.p.richLine() {
		_, _ = fmt.Fprintf(
			l.p.w,
			"%s %s\n",
			l.p.pal.info.Sprint("==>"),
			l.p.pal.bold.Sprint(name),
		)
	}
}

// OnPhaseStart lists the phase's steps (interactive only).
func (l *line) OnPhaseStart(_ string, jobs []string) {
	if l.p.richLine() && len(jobs) > 0 {
		_, _ = fmt.Fprintf(
			l.p.w,
			"  %s\n",
			l.p.pal.muted.Sprint("running: "+strings.Join(jobs, ", ")),
		)
	}
}

// OnStepStart prints nothing: a step shows when it completes.
func (*line) OnStepStart(string, string) {}

// OnStepComplete ticks the step and, for a composite step, each child (interactive
// only).
func (l *line) OnStepComplete(r types.StepResult) {
	if !l.p.richLine() {
		return
	}
	l.ticked[r.Name] = true
	_, _ = fmt.Fprintf(l.p.w, "  %s %s  %s\n", l.icon(r.Status), r.Name, l.metric(r))
	if len(r.Children) > 1 {
		for _, c := range r.Children {
			name := strings.TrimPrefix(c.Name, r.Name+"/")
			_, _ = fmt.Fprintf(
				l.p.w,
				"      %s %s  %s\n",
				l.icon(c.Status),
				name,
				l.childMetric(c),
			)
		}
	}
}

// OnPhaseComplete prints the phase's aligned status line (normal level and up).
func (l *line) OnPhaseComplete(
	name string,
	results types.StepResults,
	elapsed time.Duration,
) {
	if !l.p.at(LevelNormal) {
		return
	}
	_, _ = fmt.Fprintf(
		l.p.w, "%s %s  %s  %s\n",
		l.p.pal.info.Sprint("==>"),
		padRight(name, phaseLabelWidth),
		l.aggregate(results),
		l.p.pal.muted.Sprint(round(elapsed).String()),
	)
}

// OnGateComplete prints any unticked steps as a table (interactive only), then the
// failure block and the summary line.
func (l *line) OnGateComplete(
	_ string,
	results types.StepResults,
	elapsed time.Duration,
) {
	if l.p.richLine() {
		var unticked types.StepResults
		for _, r := range results {
			if !l.ticked[r.Name] {
				unticked = append(unticked, r)
			}
		}
		if len(unticked) > 0 {
			l.p.resultsTable(unticked)
		}
	}
	l.p.failureBlock(results)
	l.p.lineSummary(results, elapsed)
}

// icon is a status's tick glyph.
func (l *line) icon(s types.StepStatus) string {
	switch s {
	case types.StatusFail:
		return l.p.pal.failure.Sprint("✗")
	case types.StatusWarn:
		return l.p.pal.warn.Sprint("!")
	case types.StatusSkip:
		return l.p.pal.muted.Sprint("-")
	case types.StatusPass:
		return l.p.pal.success.Sprint("✓")
	default:
		return l.p.pal.success.Sprint("✓")
	}
}

// metric is a step's test count and duration, or its duration in parentheses.
func (l *line) metric(r types.StepResult) string {
	if r.Tests > 0 {
		return l.p.pal.muted.Sprintf("%d tests · %s", r.Tests, round(r.Duration))
	}
	return l.p.pal.muted.Sprintf("(%s)", round(r.Duration))
}

// childMetric is a child step's compact metric.
func (l *line) childMetric(r types.StepResult) string {
	if r.Tests > 0 {
		return l.p.pal.muted.Sprintf("%d · %s", r.Tests, round(r.Duration))
	}
	return l.p.pal.muted.Sprint(round(r.Duration).String())
}

// aggregate is a phase's status word: FAIL, warn, ok or skip.
func (l *line) aggregate(results types.StepResults) string {
	n := results.CountByStatus()
	switch {
	case n[types.StatusFail] > 0:
		return l.p.pal.failure.Sprint("FAIL")
	case n[types.StatusWarn] > 0:
		return l.p.pal.warn.Sprint("warn")
	case n[types.StatusPass] > 0:
		return l.p.pal.success.Sprint("ok  ")
	default:
		return l.p.pal.muted.Sprint("skip")
	}
}

// padRight pads s with spaces to width; a longer s is returned unchanged.
func padRight(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}
