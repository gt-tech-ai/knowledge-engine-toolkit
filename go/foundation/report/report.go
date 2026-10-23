// Package report renders a gate's progress and a CLI's console output. NewFromConfig
// builds one ExecutionObserver from config: line (a status line per phase, the
// failures and a one-line summary, with an interactive layout that ticks each step),
// table (a result table per phase), gh-actions (GitHub Actions workflow commands),
// json (one machine-readable report) or quiet (nothing, the default); Composite fans
// one run out to several. Console is a CLI's whole console: verbosity levels, a
// leveled Logger, a command hook, coded-error formatting with exit codes, and the
// observer a run should use.
package report

import (
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Kind selects a reporter.
type Kind string

const (
	// KindQuiet reports nothing (the default).
	KindQuiet Kind = "quiet"

	// KindLine prints a status line per phase on Err and ends with the failures and a
	// one-line summary; Config.Interactive adds a tick per step.
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

	// Hints are the remediation hints the failure block prints under a matching step
	// (line and table).
	Hints []Hint `yaml:"hints" mapstructure:"hints"`

	// Verbose reports at LevelVerbose rather than LevelNormal (line and table).
	Verbose bool `yaml:"verbose" mapstructure:"verbose"`

	// Interactive turns on the line reporter's interactive layout: the gate and phase
	// headers, a tick per step and a table of unticked steps. Leave it off in CI.
	Interactive bool `yaml:"interactive" mapstructure:"interactive"`

	// Color styles line and table output with ANSI color.
	Color bool `yaml:"color" mapstructure:"color"`
}

// Hint is one remediation hint: the failure block prints Text under a failed or warned
// step whose Group equals Group and whose name contains Substr (empty matches any).
type Hint struct {
	// Group is the step group the hint applies to.
	Group string `yaml:"group" mapstructure:"group"`

	// Substr narrows the hint to steps whose name contains it; empty matches every step
	// in the group.
	Substr string `yaml:"substr" mapstructure:"substr"`

	// Text is the hint line, typically the command that reproduces or fixes the step.
	Text string `yaml:"text" mapstructure:"text"`
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
	level := LevelNormal
	if cfg.Verbose {
		level = LevelVerbose
	}
	p := newPrinter(streams.Err, level, cfg.Interactive, cfg.Color, cfg.Hints)
	switch cfg.Kind {
	case "", KindQuiet:
		return quiet{}, nil
	case KindLine:
		return newLine(p), nil
	case KindTable:
		return &table{p: p}, nil
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
