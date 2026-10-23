package system

import (
	"context"
	"strings"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// LogFunc is a callback for logging command execution details.
type LogFunc func(format string, args ...any)

// Builder constructs a decorated CommandRunner using a fluent API.
// Decorators are applied inside-out: base -> env -> hook -> timeout -> logging ->
// dry-run.
type Builder struct {
	// base is the innermost runner that every decorator ultimately wraps.
	base CommandRunner

	// hook receives the CmdHook lifecycle callbacks; nil disables the hook decorator.
	hook interfaces.CmdHook

	// logf is the callback used by the logging and dry-run decorators; nil disables logging.
	logf LogFunc

	// env holds the KEY=VALUE entries added to every command; nil disables the env
	// decorator.
	env []string

	// timeout is the per-command deadline; zero disables the timeout decorator.
	timeout time.Duration

	// dryRun, when true, swaps execution for logging of what would have run.
	dryRun bool
}

// NewBuilder creates a new Builder wrapping the given base CommandRunner.
func NewBuilder(base CommandRunner) *Builder {
	return &Builder{base: base}
}

// WithLogging adds a logging decorator that logs command name, args,
// directory, duration, and error after each call.
func (b *Builder) WithLogging(logf LogFunc) *Builder {
	b.logf = logf
	return b
}

// WithTimeout adds a timeout decorator that wraps every context with the
// given deadline.
func (b *Builder) WithTimeout(d time.Duration) *Builder {
	b.timeout = d
	return b
}

// WithEnv adds an env decorator that runs every command with env (KEY=VALUE entries)
// ahead of the call's own extra env, so a call's own entries win: Run and RunBuffered
// become their WithEnv forms.
func (b *Builder) WithEnv(env ...string) *Builder {
	b.env = append(b.env, env...)
	return b
}

// WithHook adds a hook decorator that fires CmdHook lifecycle callbacks
// (OnStart, OnSuccess, OnFailure) around every command execution.
func (b *Builder) WithHook(hook interfaces.CmdHook) *Builder {
	b.hook = hook
	return b
}

// WithDryRun adds a dry-run decorator that logs what would be executed
// without actually running commands.
func (b *Builder) WithDryRun(enabled bool) *Builder {
	b.dryRun = enabled
	return b
}

// Build constructs the decorated CommandRunner. Decorators are applied
// inside-out: base -> env -> hook -> timeout -> logging -> dry-run.
func (b *Builder) Build() CommandRunner {
	r := b.base

	if len(b.env) > 0 {
		r = &envRunner{inner: r, env: b.env}
	}
	if b.hook != nil {
		r = &hookRunner{inner: r, hook: b.hook}
	}
	if b.timeout > 0 {
		r = &timeoutRunner{inner: r, timeout: b.timeout}
	}
	if b.logf != nil {
		r = &loggingRunner{inner: r, logf: b.logf}
	}
	if b.dryRun {
		r = &dryRunRunner{inner: r, logf: b.logf}
	}

	return r
}

// ---------------------------------------------------------------------------
// envRunner
// ---------------------------------------------------------------------------

// envRunner decorates a CommandRunner, adding env to every command it runs.
type envRunner struct {
	// inner runs the commands.
	inner CommandRunner

	// env holds the KEY=VALUE entries added to every command.
	env []string
}

// with returns env followed by extra, so a command's own entries win.
func (d *envRunner) with(extra []string) []string {
	return append(append([]string{}, d.env...), extra...)
}

// Run runs the command with env added.
func (d *envRunner) Run(ctx context.Context, dir, name string, args ...string) error {
	return d.inner.RunWithEnv(ctx, dir, d.with(nil), name, args...)
}

// RunWithEnv runs the command with env, then extraEnv, added.
func (d *envRunner) RunWithEnv(
	ctx context.Context,
	dir string,
	extraEnv []string,
	name string,
	args ...string,
) error {
	return d.inner.RunWithEnv(ctx, dir, d.with(extraEnv), name, args...)
}

// RunBuffered runs the command with env added and its output captured.
func (d *envRunner) RunBuffered(
	ctx context.Context,
	dir, name string,
	args ...string,
) CmdResult {
	return d.inner.RunBufferedWithEnv(ctx, dir, d.with(nil), name, args...)
}

// RunBufferedWithEnv runs the command with env, then extraEnv, added and its output
// captured.
func (d *envRunner) RunBufferedWithEnv(
	ctx context.Context,
	dir string,
	extraEnv []string,
	name string,
	args ...string,
) CmdResult {
	return d.inner.RunBufferedWithEnv(ctx, dir, d.with(extraEnv), name, args...)
}

// Exists delegates to the inner runner.
func (d *envRunner) Exists(name string) bool { return d.inner.Exists(name) }

// RequireTool delegates to the inner runner.
func (d *envRunner) RequireTool(name, installHint string) error {
	return d.inner.RequireTool(name, installHint)
}

// ---------------------------------------------------------------------------
// loggingRunner
// ---------------------------------------------------------------------------

// loggingRunner decorates a CommandRunner, logging the command, args, directory,
// duration, and error after each call via logf.
type loggingRunner struct {
	// inner is the wrapped runner that performs the actual execution.
	inner CommandRunner

	// logf records each command's lifecycle line.
	logf LogFunc
}

// Run delegates to the inner runner and logs the command's outcome and duration.
func (d *loggingRunner) Run(ctx context.Context, dir, name string, args ...string) error {
	start := time.Now()
	err := d.inner.Run(ctx, dir, name, args...)
	d.logf("[runner] %s %s dir=%s duration=%s err=%v",
		name, strings.Join(args, " "), dir, time.Since(start), err)
	return err
}

// RunWithEnv delegates to the inner runner with extra environment variables and
// logs the command's outcome and duration.
func (d *loggingRunner) RunWithEnv(
	ctx context.Context,
	dir string,
	extraEnv []string,
	name string,
	args ...string,
) error {
	start := time.Now()
	err := d.inner.RunWithEnv(ctx, dir, extraEnv, name, args...)
	d.logf("[runner] %s %s dir=%s duration=%s err=%v",
		name, strings.Join(args, " "), dir, time.Since(start), err)
	return err
}

// RunBuffered delegates to the inner runner and logs the buffered command's
// outcome and duration.
func (d *loggingRunner) RunBuffered(
	ctx context.Context,
	dir, name string,
	args ...string,
) CmdResult {
	start := time.Now()
	cr := d.inner.RunBuffered(ctx, dir, name, args...)
	d.logf("[runner] %s %s dir=%s duration=%s err=%v",
		name, strings.Join(args, " "), dir, time.Since(start), cr.Err)
	return cr
}

// RunBufferedWithEnv delegates to the inner runner with extra environment
// variables and logs the buffered command's outcome and duration.
func (d *loggingRunner) RunBufferedWithEnv(
	ctx context.Context,
	dir string,
	extraEnv []string,
	name string,
	args ...string,
) CmdResult {
	start := time.Now()
	cr := d.inner.RunBufferedWithEnv(ctx, dir, extraEnv, name, args...)
	d.logf("[runner] %s %s dir=%s duration=%s err=%v",
		name, strings.Join(args, " "), dir, time.Since(start), cr.Err)
	return cr
}

// Exists delegates to the inner runner (read-only check, not logged).
func (d *loggingRunner) Exists(name string) bool {
	return d.inner.Exists(name)
}

// RequireTool delegates to the inner runner (read-only check, not logged).
func (d *loggingRunner) RequireTool(name, installHint string) error {
	return d.inner.RequireTool(name, installHint)
}

// ---------------------------------------------------------------------------
// timeoutRunner
// ---------------------------------------------------------------------------

// timeoutRunner decorates a CommandRunner, wrapping every execution context with
// a fixed deadline so a hung command is cancelled after timeout elapses.
type timeoutRunner struct {
	// inner is the wrapped runner whose calls are bounded by the timeout.
	inner CommandRunner

	// timeout is the deadline applied to every command's context.
	timeout time.Duration
}

// Run wraps the context with the configured deadline and delegates to the inner runner.
func (d *timeoutRunner) Run(ctx context.Context, dir, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.inner.Run(ctx, dir, name, args...)
}

// RunWithEnv wraps the context with the configured deadline and delegates to the
// inner runner with extra environment variables.
func (d *timeoutRunner) RunWithEnv(
	ctx context.Context,
	dir string,
	extraEnv []string,
	name string,
	args ...string,
) error {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.inner.RunWithEnv(ctx, dir, extraEnv, name, args...)
}

// RunBuffered wraps the context with the configured deadline and delegates the
// buffered execution to the inner runner.
func (d *timeoutRunner) RunBuffered(
	ctx context.Context,
	dir, name string,
	args ...string,
) CmdResult {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.inner.RunBuffered(ctx, dir, name, args...)
}

// RunBufferedWithEnv wraps the context with the configured deadline and delegates
// the buffered execution to the inner runner with extra environment variables.
func (d *timeoutRunner) RunBufferedWithEnv(
	ctx context.Context,
	dir string,
	extraEnv []string,
	name string,
	args ...string,
) CmdResult {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.inner.RunBufferedWithEnv(ctx, dir, extraEnv, name, args...)
}

// Exists delegates to the inner runner (read-only check, no timeout applied).
func (d *timeoutRunner) Exists(name string) bool {
	return d.inner.Exists(name)
}

// RequireTool delegates to the inner runner (read-only check, no timeout applied).
func (d *timeoutRunner) RequireTool(name, installHint string) error {
	return d.inner.RequireTool(name, installHint)
}

// ---------------------------------------------------------------------------
// dryRunRunner
// ---------------------------------------------------------------------------

// dryRunRunner decorates a CommandRunner, logging what each mutating call would
// execute instead of running it; read-only checks still pass through to inner.
type dryRunRunner struct {
	// inner is the wrapped runner, used only for the read-only Exists/RequireTool checks.
	inner CommandRunner

	// logf reports the would-execute line; nil suppresses the log.
	logf LogFunc
}

// log emits the "would execute" line for a command when a logger is configured.
func (d *dryRunRunner) log(name string, args ...string) {
	if d.logf != nil {
		d.logf("[dry-run] would execute: %s %s", name, strings.Join(args, " "))
	}
}

// Run logs the command instead of executing it and returns nil.
func (d *dryRunRunner) Run(_ context.Context, _, name string, args ...string) error {
	d.log(name, args...)
	return nil
}

// RunWithEnv logs the command instead of executing it and returns nil.
func (d *dryRunRunner) RunWithEnv(
	_ context.Context,
	_ string,
	_ []string,
	name string,
	args ...string,
) error {
	d.log(name, args...)
	return nil
}

// RunBuffered logs the command instead of executing it and returns a zero CmdResult.
func (d *dryRunRunner) RunBuffered(
	_ context.Context,
	_, name string,
	args ...string,
) CmdResult {
	d.log(name, args...)
	return CmdResult{}
}

// RunBufferedWithEnv logs the command instead of executing it and returns a zero
// CmdResult.
func (d *dryRunRunner) RunBufferedWithEnv(
	_ context.Context,
	_ string,
	_ []string,
	name string,
	args ...string,
) CmdResult {
	d.log(name, args...)
	return CmdResult{}
}

// Exists delegates to the inner runner (read-only check).
func (d *dryRunRunner) Exists(name string) bool {
	return d.inner.Exists(name)
}

// RequireTool delegates to the inner runner (read-only check).
func (d *dryRunRunner) RequireTool(name, installHint string) error {
	return d.inner.RequireTool(name, installHint)
}

// ---------------------------------------------------------------------------
// hookRunner
// ---------------------------------------------------------------------------

// hookRunner fires CmdHook lifecycle callbacks around each command execution.
type hookRunner struct {
	// inner is the wrapped runner that performs the actual execution.
	inner CommandRunner

	// hook receives OnStart, OnSuccess, and OnFailure for each command.
	hook interfaces.CmdHook
}

// cmdStr renders a command and its args as a single display string for the hook.
func (h *hookRunner) cmdStr(name string, args []string) string {
	if len(args) == 0 {
		return name
	}
	return name + " " + strings.Join(args, " ")
}

// Run fires OnStart, delegates to the inner runner, then fires OnSuccess or
// OnFailure depending on the result.
func (h *hookRunner) Run(ctx context.Context, dir, name string, args ...string) error {
	cs := h.cmdStr(name, args)
	h.hook.OnStart(cs)
	err := h.inner.Run(ctx, dir, name, args...)
	if err != nil {
		h.hook.OnFailure(cs)
	} else {
		h.hook.OnSuccess(cs)
	}
	return err
}

// RunWithEnv fires OnStart, delegates to the inner runner with extra environment
// variables, then fires OnSuccess or OnFailure depending on the result.
func (h *hookRunner) RunWithEnv(
	ctx context.Context,
	dir string,
	extraEnv []string,
	name string,
	args ...string,
) error {
	cs := h.cmdStr(name, args)
	h.hook.OnStart(cs)
	err := h.inner.RunWithEnv(ctx, dir, extraEnv, name, args...)
	if err != nil {
		h.hook.OnFailure(cs)
	} else {
		h.hook.OnSuccess(cs)
	}
	return err
}

// RunBuffered fires OnStart, delegates the buffered execution to the inner runner,
// then fires OnSuccess or OnFailure depending on the result.
func (h *hookRunner) RunBuffered(
	ctx context.Context,
	dir, name string,
	args ...string,
) CmdResult {
	cs := h.cmdStr(name, args)
	h.hook.OnStart(cs)
	cr := h.inner.RunBuffered(ctx, dir, name, args...)
	if cr.Err != nil {
		h.hook.OnFailure(cs)
	} else {
		h.hook.OnSuccess(cs)
	}
	return cr
}

// RunBufferedWithEnv fires OnStart, delegates the buffered execution to the inner
// runner with extra environment variables, then fires OnSuccess or OnFailure
// depending on the result.
func (h *hookRunner) RunBufferedWithEnv(
	ctx context.Context,
	dir string,
	extraEnv []string,
	name string,
	args ...string,
) CmdResult {
	cs := h.cmdStr(name, args)
	h.hook.OnStart(cs)
	cr := h.inner.RunBufferedWithEnv(ctx, dir, extraEnv, name, args...)
	if cr.Err != nil {
		h.hook.OnFailure(cs)
	} else {
		h.hook.OnSuccess(cs)
	}
	return cr
}

// Exists delegates to the inner runner (read-only check, no hook fired).
func (h *hookRunner) Exists(name string) bool { return h.inner.Exists(name) }

// RequireTool delegates to the inner runner (read-only check, no hook fired).
func (h *hookRunner) RequireTool(
	n, hint string,
) error {
	return h.inner.RequireTool(n, hint)
}
