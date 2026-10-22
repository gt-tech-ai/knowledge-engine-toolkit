// Package report builds an ExecutionObserver from config: line (live ticks and a
// summary), table (a result table per phase), gh-actions (GitHub Actions workflow
// commands), json (one machine-readable report) or quiet (nothing, the default), and
// composes several into one.
package report

import (
	"fmt"
	"io"
	"strings"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Kind selects a reporter.
type Kind string

const (
	// KindQuiet reports nothing (the default).
	KindQuiet Kind = "quiet"

	// KindLine ticks each step on Err as it completes and ends with the failures and a
	// one-line summary.
	KindLine Kind = "line"

	// KindTable prints a result table per phase and for the gate on Err.
	KindTable Kind = "table"

	// KindGHActions writes GitHub Actions log groups and annotations on Out.
	KindGHActions Kind = "gh-actions"

	// KindJSON writes one JSON report of the gate on Out.
	KindJSON Kind = "json"
)

// Config selects and tunes a reporter.
type Config struct {
	// Kind selects the reporter; empty means KindQuiet.
	Kind Kind `yaml:"kind" mapstructure:"kind"`

	// Verbose shows every step's detail, not only failures' (line and table).
	Verbose bool `yaml:"verbose" mapstructure:"verbose"`
}

// NewFromConfig builds the reporter cfg.Kind selects over streams. An unknown kind, or
// streams missing a writer, is CodeInvalidInput.
func NewFromConfig(
	cfg Config,
	streams types.Streams,
) (interfaces.ExecutionObserver, error) {
	if err := streams.Require(); err != nil {
		return nil, err
	}
	switch cfg.Kind {
	case "", KindQuiet:
		return quiet{}, nil
	case KindLine:
		return &line{w: streams.Err, verbose: cfg.Verbose}, nil
	case KindTable:
		return &table{w: streams.Err, verbose: cfg.Verbose}, nil
	case KindGHActions:
		return &ghActions{w: streams.Out}, nil
	case KindJSON:
		return &jsonReport{w: streams.Out}, nil
	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"report: unknown kind "+string(cfg.Kind),
		)
	}
}

// icon is a status's glyph.
func icon(s types.StepStatus) string {
	switch s {
	case types.StatusFail:
		return "✗"
	case types.StatusWarn:
		return "!"
	case types.StatusSkip:
		return "-"
	case types.StatusPass:
		return "✓"
	default:
		return "?"
	}
}

// round shortens a duration for display.
func round(d time.Duration) time.Duration { return d.Round(time.Millisecond) }

// summary is the "N ok, M failed, … (elapsed)" line.
func summary(results types.StepResults, elapsed time.Duration) string {
	counts := results.CountByStatus()
	var parts []string
	for _, c := range []struct {
		label  string
		status types.StepStatus
	}{
		{"ok", types.StatusPass},
		{"failed", types.StatusFail},
		{"warned", types.StatusWarn},
		{"skipped", types.StatusSkip},
	} {
		if n := counts[c.status]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, c.label))
		}
	}
	if len(parts) == 0 {
		parts = []string{"no steps"}
	}
	return fmt.Sprintf("%s (%s)", strings.Join(parts, ", "), round(elapsed))
}

// failures writes each failed step's error and indented detail.
func failures(w io.Writer, results types.StepResults) {
	for _, r := range results {
		if r.Status != types.StatusFail {
			continue
		}
		_, _ = fmt.Fprintf(w, "\n%s %s: %s\n", icon(r.Status), r.Name, r.Error)
		indent(w, r.Detail)
	}
}

// indent writes detail's non-empty lines indented by four spaces.
func indent(w io.Writer, detail []byte) {
	for l := range strings.SplitSeq(strings.TrimSpace(string(detail)), "\n") {
		if l != "" {
			_, _ = fmt.Fprintf(w, "    %s\n", l)
		}
	}
}
