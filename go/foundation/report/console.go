package report

import (
	"context"
	"fmt"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// ConsoleConfig tunes a Console. A composition root resolves it from the environment
// (a TTY, NO_COLOR, CI, GITHUB_STEP_SUMMARY); the console itself reads nothing.
type ConsoleConfig struct {
	// StepSummaryPath is the GitHub step summary file a run's verdict is appended to
	// when its gate completes; empty writes none.
	StepSummaryPath string `yaml:"step_summary_path" mapstructure:"step_summary_path"`

	// Hints are the failure block's remediation hints.
	Hints []Hint `yaml:"hints" mapstructure:"hints"`

	// Color styles the output with ANSI color.
	Color bool `yaml:"color" mapstructure:"color"`

	// CI marks a CI run: the line reporter drops its interactive layout and GitHub
	// Actions annotations join it.
	CI bool `yaml:"ci" mapstructure:"ci"`

	// GitHubActions marks a run inside GitHub Actions: Groupf opens a collapsible log
	// group there.
	GitHubActions bool `yaml:"github_actions" mapstructure:"github_actions"`
}

// Console is a CLI's console on one error stream: a verbosity level, a leveled Logger,
// a command hook, coded-error formatting, and the observer a gate run reports through.
// One Console is built at the composition root and shared; SetLevel may change its
// verbosity after the reporters it built exist.
type Console struct {
	// p is the printer the console and its reporters share.
	p *printer

	// cfg is the console's configuration.
	cfg ConsoleConfig
}

// NewConsole returns a Console writing to streams.Err at LevelNormal. Streams missing a
// writer are CodeInvalidInput.
func NewConsole(streams types.Streams, cfg ConsoleConfig) (*Console, error) {
	if err := streams.Require(); err != nil {
		return nil, err
	}
	return &Console{
		p:   newPrinter(streams.Err, LevelNormal, !cfg.CI, cfg.Color, cfg.Hints),
		cfg: cfg,
	}, nil
}

// SetLevel sets the console's verbosity.
func (c *Console) SetLevel(l Level) { c.p.level.Store(int32(l)) }

// Level returns the console's verbosity.
func (c *Console) Level() Level { return Level(c.p.level.Load()) }

// Groupf opens a section titled by format: a collapsible log group under GitHub
// Actions, otherwise a bold header at the verbose level only (the line reporter's phase
// lines already title a normal run).
func (c *Console) Groupf(format string, a ...any) {
	if c.cfg.GitHubActions {
		_, _ = fmt.Fprintf(c.p.w, "::group::%s\n", fmt.Sprintf(format, a...))
		return
	}
	if c.p.at(LevelVerbose) {
		c.p.headerf(format, a...)
	}
}

// EndGroup closes the section Groupf opened; outside GitHub Actions it prints nothing.
func (c *Console) EndGroup() {
	if c.cfg.GitHubActions {
		_, _ = fmt.Fprintln(c.p.w, "::endgroup::")
	}
}

// Debugf prints a muted debug line (verbose only).
func (c *Console) Debugf(format string, a ...any) {
	if c.p.at(LevelVerbose) {
		c.p.pal.muted.Fprintf(c.p.w, format+"\n", a...)
	}
}

// Infof prints an informational line (normal level and up).
func (c *Console) Infof(format string, a ...any) {
	if c.p.at(LevelNormal) {
		c.p.pal.info.Fprintf(c.p.w, format+"\n", a...)
	}
}

// Successf prints a success line (normal level and up).
func (c *Console) Successf(format string, a ...any) {
	if c.p.at(LevelNormal) {
		c.p.pal.success.Fprintf(c.p.w, format+"\n", a...)
	}
}

// Warnf prints a warning line (normal level and up).
func (c *Console) Warnf(format string, a ...any) {
	if c.p.at(LevelNormal) {
		c.p.pal.warn.Fprintf(c.p.w, format+"\n", a...)
	}
}

// Errorf prints an error line at every level.
func (c *Console) Errorf(format string, a ...any) {
	c.p.pal.failure.Fprintf(c.p.w, format+"\n", a...)
}

// Observer returns the reporter a gate run should use at the console's current level:
// the table when verbose, the line reporter with GitHub Actions annotations in CI, and
// the interactive line reporter otherwise. With a step summary path set, the run's
// verdict is also appended there.
func (c *Console) Observer() interfaces.ExecutionObserver {
	var obs interfaces.ExecutionObserver
	switch {
	case c.p.at(LevelVerbose):
		obs = &table{p: c.p}
	case c.cfg.CI:
		obs = Composite(newLine(c.p), &ghActions{w: c.p.w})
	default:
		obs = newLine(c.p)
	}
	if c.cfg.StepSummaryPath != "" {
		obs = Composite(obs, &stepSummary{path: c.cfg.StepSummaryPath})
	}
	return obs
}

// FormatError prints err and returns the process exit code for it: a coded error
// prints as "<CODE>: <message>" (with its cause when verbose) and exits with
// ExitCode(code); any other error prints as "Error: <err>" and exits 1.
func (c *Console) FormatError(err error) int {
	var appErr *apperr.AppError
	if apperr.As(err, &appErr) {
		c.Errorf("%s: %s", appErr.Code, appErr.Message)
		if appErr.Err != nil {
			c.Debugf("  cause: %v", appErr.Err)
		}
		return ExitCode(appErr.Code)
	}
	c.Errorf("Error: %v", err)
	return 1
}

// ExitCode maps an error code to a process exit code: 2 for a failure the operator can
// act on (invalid input, not found, quality failed), 3 for an unavailable operation,
// and 1 otherwise.
func ExitCode(code apperr.ErrorCode) int {
	switch code {
	case apperr.CodeInvalidInput, apperr.CodeNotFound, apperr.CodeQualityFailed:
		return 2
	case apperr.CodeUnavailable:
		return 3
	case apperr.CodeUnknown,
		apperr.CodeInternal,
		apperr.CodeUnauthorized,
		apperr.CodeForbidden,
		apperr.CodeConflict,
		apperr.CodeTimeout,
		apperr.CodeCanceled,
		apperr.CodeIngestion,
		apperr.CodeUpstream,
		apperr.CodeResourceExhausted:
		return 1
	default:
		return 1
	}
}

// CommandHook returns a CmdHook that echoes each command the runner starts, and its
// outcome, at the verbose level.
func (c *Console) CommandHook() interfaces.CmdHook { return hook{c: c} }

// hook is the console's command hook.
type hook struct {
	// c is the console it prints to.
	c *Console
}

// OnStart prints "  > <cmd>" (verbose only).
func (h hook) OnStart(cmd string) { h.print(h.c.p.pal.info, "  > %s\n", cmd) }

// OnSuccess prints "  ok <cmd>" (verbose only).
func (h hook) OnSuccess(cmd string) { h.print(h.c.p.pal.success, "  ok %s\n", cmd) }

// OnFailure prints "  FAIL <cmd>" (verbose only).
func (h hook) OnFailure(cmd string) { h.print(h.c.p.pal.failure, "  FAIL %s\n", cmd) }

// print writes one styled hook line when the console is verbose.
func (h hook) print(s style, format, cmd string) {
	if h.c.p.at(LevelVerbose) {
		s.Fprintf(h.c.p.w, format, cmd)
	}
}

// Logger returns an interfaces.Logger that prints through the console: Debug at the
// verbose level, Info and Warn at the normal level, Error always. Key-value pairs are
// appended to the message as " key=value".
func (c *Console) Logger() interfaces.Logger { return &consoleLogger{c: c} }

// consoleLogger is the console's Logger.
type consoleLogger struct {
	// c is the console it prints to.
	c *Console

	// kvs are the pairs bound with With.
	kvs []any
}

// Debug prints msg at the verbose level.
func (l *consoleLogger) Debug(
	msg string,
	kvs ...any,
) {
	l.c.Debugf("%s", l.format(msg, kvs))
}

// Info prints msg at the normal level.
func (l *consoleLogger) Info(
	msg string,
	kvs ...any,
) {
	l.c.Infof("%s", l.format(msg, kvs))
}

// Warn prints msg at the normal level.
func (l *consoleLogger) Warn(
	msg string,
	kvs ...any,
) {
	l.c.Warnf("%s", l.format(msg, kvs))
}

// Error prints msg at every level.
func (l *consoleLogger) Error(
	msg string,
	kvs ...any,
) {
	l.c.Errorf("%s", l.format(msg, kvs))
}

// With returns a logger that appends kvs to every message after the pairs already bound.
func (l *consoleLogger) With(kvs ...any) interfaces.Logger {
	merged := make([]any, 0, len(l.kvs)+len(kvs))
	merged = append(append(merged, l.kvs...), kvs...)
	return &consoleLogger{c: l.c, kvs: merged}
}

// WithContext returns the logger unchanged: the console carries no trace context.
func (l *consoleLogger) WithContext(context.Context) interfaces.Logger { return l }

// format appends the bound pairs, then kvs, to msg; each list is paired on its own, so
// an odd bound list never shifts the call-site pairs.
func (l *consoleLogger) format(msg string, kvs []any) string {
	if len(l.kvs) == 0 && len(kvs) == 0 {
		return msg
	}
	var sb strings.Builder
	sb.WriteString(msg)
	for _, list := range [][]any{l.kvs, kvs} {
		for i := 0; i+1 < len(list); i += 2 {
			_, _ = fmt.Fprintf(&sb, " %v=%v", list[i], list[i+1])
		}
	}
	return sb.String()
}
