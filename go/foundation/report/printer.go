package report

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// maxMessageLen is the longest message printed before truncation.
const maxMessageLen = 100

// Level is a console verbosity.
type Level int32

const (
	// LevelQuiet shows only errors.
	LevelQuiet Level = iota

	// LevelNormal shows progress, results, warnings and errors.
	LevelNormal

	// LevelVerbose also shows debug lines, commands and per-phase result tables.
	LevelVerbose
)

// actionableLineLimit caps the output lines a failure block shows per step.
const actionableLineLimit = 10

// printer is the rendering state the console and its line and table reporters share:
// one writer, one verbosity, the palette and the remediation hints.
type printer struct {
	// w receives everything the printer writes.
	w io.Writer

	// pal styles the output.
	pal palette

	// hints are the failure block's remediation hints, first match wins.
	hints []Hint

	// level is the verbosity; atomic so a front-end may change it while a reporter
	// built from the same console is live.
	level atomic.Int32

	// rich turns on the interactive line layout: the gate and phase headers, a tick
	// per step and the table of unticked steps.
	rich bool
}

// lockedWriter serializes writes to w, so the goroutines sharing a console (a
// procedure's probes, a suite's fan-out) never race on it, and each styled block, written
// in one call, lands whole.
type lockedWriter struct {
	// w receives the writes.
	w io.Writer

	// mu guards w.
	mu sync.Mutex
}

// Write writes b to w under the lock.
func (l *lockedWriter) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	written, err := l.w.Write(b)
	if err != nil {
		return written, apperr.Wrap(err, apperr.CodeUnavailable, "report: write output")
	}

	return written, nil
}

// newPrinter returns a printer over w at level; writes are serialized (lockedWriter).
func newPrinter(w io.Writer, level Level, rich, color bool, hints []Hint) *printer {
	currentPrinter := &printer{
		w:     &lockedWriter{w: w, mu: sync.Mutex{}},
		rich:  rich,
		pal:   newPalette(color),
		hints: hints,
		level: atomic.Int32{},
	}
	currentPrinter.level.Store(int32(level))

	return currentPrinter
}

// at reports whether the printer's verbosity is at least l.
func (p *printer) at(l Level) bool { return Level(p.level.Load()) >= l }

// richLine reports whether the line reporter draws its interactive layout.
func (p *printer) richLine() bool { return p.rich && p.at(LevelNormal) }

// headerf prints a bold section header (normal level and up).
func (p *printer) headerf(format string, a ...any) {
	if p.at(LevelNormal) {
		p.pal.bold.Fprintf(p.w, "\n-- %s --\n\n", fmt.Sprintf(format, a...))
	}
}

// status is a status's table cell.
func (p *printer) status(s types.StepStatus) string {
	switch s {
	case types.StatusPass:
		return p.pal.success.Sprint("PASS")
	case types.StatusFail:
		return p.pal.failure.Sprint("FAIL")
	case types.StatusSkip:
		return p.pal.muted.Sprint("SKIP")
	case types.StatusWarn:
		return p.pal.warn.Sprint("WARN")
	default:
		return "????"
	}
}

// resultsTable prints results as a STEP / STATUS / DURATION / ERROR table grouped by
// each result's Group (normal level and up).
func (p *printer) resultsTable(results types.StepResults) {
	if !p.at(LevelNormal) {
		return
	}

	_, _ = fmt.Fprintln(p.w)
	_, _ = fmt.Fprintf(
		p.w,
		"  %-30s %-8s %10s  %s\n",
		"STEP",
		"STATUS",
		"DURATION",
		"ERROR",
	)
	_, _ = fmt.Fprintf(
		p.w, "  %-30s %-8s %10s  %s\n",
		"──────────────────────────────", "──────", "────────", "─────",
	)

	var group string

	for _, stepResult := range results {
		if stepResult.Group != group {
			group = stepResult.Group
			_, _ = fmt.Fprintln(p.w)
			p.pal.bold.Fprintf(p.w, "  %s\n", group)
		}

		msg := stripCode(stepResult.Error)
		if len(msg) > maxMessageLen {
			msg = msg[:maxMessageLen-len("...")] + "..."
		}

		_, _ = fmt.Fprintf(
			p.w, "    %-28s %-8s %10s  %s\n",
			stepResult.Name, p.status(stepResult.Status), round(stepResult.Duration), msg,
		)
	}

	_, _ = fmt.Fprintln(p.w)
}

// total prints the "TOTAL: n passed, … (elapsed)" line under a table (normal level
// and up). A zero elapsed falls back to the sum of the step durations.
func (p *printer) total(results types.StepResults, elapsed time.Duration) {
	if !p.at(LevelNormal) {
		return
	}

	if elapsed <= 0 {
		for _, r := range results {
			elapsed += r.Duration
		}
	}

	_, _ = fmt.Fprintf(
		p.w,
		"  TOTAL: %s (%s)\n\n",
		p.counts(results, "passed"),
		round(elapsed),
	)
}

// counts joins the non-zero status counts, labelling passes as passed.
func (p *printer) counts(results types.StepResults, passed string) string {
	lookup := results.CountByStatus()

	var parts []string

	for _, item := range []struct {
		label  string
		status types.StepStatus
	}{
		{passed, types.StatusPass},
		{"failed", types.StatusFail},
		{"warned", types.StatusWarn},
		{"skipped", types.StatusSkip},
	} {
		if lookup[item.status] > 0 {
			parts = append(
				parts,
				p.countStyle(item.status).
					Sprintf("%d %s", lookup[item.status], item.label),
			)
		}
	}

	return strings.Join(parts, ", ")
}

// countStyle is the style a status's count prints in.
func (p *printer) countStyle(s types.StepStatus) style {
	switch s {
	case types.StatusPass:
		return p.pal.success
	case types.StatusFail:
		return p.pal.failure
	case types.StatusWarn:
		return p.pal.warn
	case types.StatusSkip:
		return p.pal.muted
	default:
		// Any other value takes the shared result below.
	}

	return p.pal.muted
}

// lineSummary prints the line reporter's closing "n ok, … (elapsed)" line (normal
// level and up).
func (p *printer) lineSummary(results types.StepResults, elapsed time.Duration) {
	if !p.at(LevelNormal) {
		return
	}

	summary := p.counts(results, "ok")
	if summary == "" {
		summary = "no steps"
	}

	_, _ = fmt.Fprintf(
		p.w,
		"\n%s  %s\n",
		summary,
		p.pal.muted.Sprintf("(%s)", round(elapsed)),
	)
}

// failureBlock prints each failed and warned step with its actionable output lines
// and its remediation hint (normal level and up).
func (p *printer) failureBlock(results types.StepResults) {
	if !p.at(LevelNormal) {
		return
	}

	flagged, failed := flaggedResults(results)
	if len(flagged) == 0 {
		return
	}

	heading := "Warnings:"
	if failed {
		heading = "Failures:"
	}

	p.pal.bold.Fprintf(p.w, "  %s\n", heading)

	for _, r := range flagged {
		p.stepDetail(r)
	}

	_, _ = fmt.Fprintln(p.w)
}

// flaggedResults picks the failed and warned results, reporting whether any failed.
func flaggedResults(results types.StepResults) ([]*types.StepResult, bool) {
	var flagged []*types.StepResult

	failed := false

	for i := range results {
		r := &results[i]
		if r.Status == types.StatusFail || r.Status == types.StatusWarn {
			flagged = append(flagged, r)
			failed = failed || r.Status == types.StatusFail
		}
	}

	return flagged, failed
}

// stepDetail prints one flagged step: its name, the actionable lines of its output
// (or its error, split into clauses, when the output has none) and its hint.
func (p *printer) stepDetail(stepResult *types.StepResult) {
	label := p.pal.failure.Sprint("FAIL")
	if stepResult.Status == types.StatusWarn {
		label = p.pal.warn.Sprint("WARN")
	}

	_, _ = fmt.Fprintf(p.w, "\n    %s %s\n", label, p.pal.bold.Sprint(stepResult.Name))

	lines := ActionableLines(stepResult.Detail, actionableLineLimit)
	if len(lines) == 0 && stepResult.Error != "" {
		lines = errorClauses(stepResult.Error)
	}

	for _, l := range lines {
		_, _ = fmt.Fprintf(p.w, "      %s\n", l)
	}

	if hint := p.hint(stepResult); hint != "" {
		_, _ = fmt.Fprintf(p.w, "      %s\n", p.pal.info.Sprint(hint))
	}
}

// errorClauses splits an error on newlines and semicolons into truncated clauses.
func errorClauses(msg string) []string {
	var lines []string

	for part := range strings.SplitSeq(msg, "\n") {
		for clause := range strings.SplitSeq(strings.TrimSpace(part), "; ") {
			if clause = strings.TrimSpace(clause); clause != "" {
				lines = append(lines, truncate(clause))
			}
		}
	}

	return lines
}

// hint returns the first hint whose group equals r's and whose name fragment (empty
// matches any) r's name contains.
func (p *printer) hint(r *types.StepResult) string {
	for _, h := range p.hints {
		if r.Group == h.Group && (h.Substr == "" || strings.Contains(r.Name, h.Substr)) {
			return h.Text
		}
	}

	return ""
}

// round shortens a duration for display.
func round(d time.Duration) time.Duration { return d.Round(time.Millisecond) }

// stripCode drops a leading error code ("INVALID_INPUT: ") from a step error for
// display.
func stripCode(msg string) string {
	if _, rest, ok := apperr.SplitCode(msg); ok {
		return rest
	}

	return msg
}
