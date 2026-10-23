// Package system provides CommandRunner implementations for executing external
// commands. The canonical CommandRunner interface is defined in core/interfaces;
// this package provides concrete runners and decorator builders.
package system

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// CommandRunner is a type alias for interfaces.CommandRunner.
// system.CommandRunner and interfaces.CommandRunner are identical types;
// no cast or conversion is needed at any call site.
type CommandRunner = interfaces.CommandRunner

// CmdResult is a type alias for interfaces.CmdResult.
type CmdResult = interfaces.CmdResult

// Compile-time interface compliance check.
var _ interfaces.CommandRunner = (*Runner)(nil)

// Runner implements CommandRunner using real command execution. Run and RunWithEnv
// stream the child's stdout/stderr to the configured writers (WithOutput) — by
// default the process's os.Stdout/os.Stderr as they are at Run time, so terminal
// output flows directly to the user.
type Runner struct {
	// stdout receives a streamed child's stdout; nil means os.Stdout at Run time.
	stdout io.Writer

	// stderr receives a streamed child's stderr; nil means os.Stderr at Run time.
	stderr io.Writer
}

// RunnerOption configures a Runner at construction.
type RunnerOption func(*Runner)

// WithOutput streams a child's stdout to stdout and its stderr to stderr instead of
// the process's own stdio — for a caller that owns the process stdout (a stdio
// protocol server) or captures output. A nil writer keeps that stream's default.
// The buffered calls (RunBuffered*) capture into their result and are unaffected.
func WithOutput(stdout, stderr io.Writer) RunnerOption {
	return func(r *Runner) {
		r.stdout = stdout
		r.stderr = stderr
	}
}

// NewRunner creates a Runner for real command execution, configured by opts.
func NewRunner(opts ...RunnerOption) *Runner {
	r := &Runner{}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Run executes a command, streaming its stdout/stderr to the runner's writers.
func (d *Runner) Run(ctx context.Context, dir, name string, args ...string) error {
	return d.runWithEnv(ctx, dir, nil, name, args...)
}

// RunWithEnv executes a command with extra environment variables appended.
func (d *Runner) RunWithEnv(
	ctx context.Context,
	dir string,
	extraEnv []string,
	name string,
	args ...string,
) error {
	return d.runWithEnv(ctx, dir, extraEnv, name, args...)
}

// RunBuffered executes a command with stdout/stderr captured into buffers.
func (d *Runner) RunBuffered(
	ctx context.Context,
	dir, name string,
	args ...string,
) interfaces.CmdResult {
	return d.runBuffered(ctx, dir, nil, name, args...)
}

// RunBufferedWithEnv executes a command with extra environment variables
// appended and stdout/stderr captured into buffers.
func (d *Runner) RunBufferedWithEnv(
	ctx context.Context,
	dir string,
	extraEnv []string,
	name string,
	args ...string,
) interfaces.CmdResult {
	return d.runBuffered(ctx, dir, extraEnv, name, args...)
}

// runBuffered is the shared implementation for RunBuffered and
// RunBufferedWithEnv.
func (d *Runner) runBuffered(
	ctx context.Context,
	dir string,
	extraEnv []string,
	name string,
	args ...string,
) interfaces.CmdResult {
	var stdout, stderr bytes.Buffer

	cmd := exec.CommandContext(
		ctx,
		name,
		args...,
	)
	cmd.Dir = dir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = types.MergeEnv(os.Environ(), extraEnv)
	configureProcessGroup(cmd)

	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start)

	exitCode := 0
	if err != nil {
		// ProcessState is set when the process ran and exited (incl. non-zero);
		// nil when it never started (e.g. binary not found) — report -1 then.
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		} else {
			exitCode = -1
		}
	}

	return interfaces.CmdResult{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		Duration: dur,
		Err:      err,
		ExitCode: exitCode,
	}
}

// Exists checks whether a binary is available on PATH.
func (d *Runner) Exists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// RequireTool returns an error if the named binary is not on PATH.
func (d *Runner) RequireTool(name, installHint string) error {
	if d.Exists(name) {
		return nil
	}
	msg := name + " not found on PATH"
	if installHint != "" {
		msg = fmt.Sprintf("%s not found on PATH; install: %s", name, installHint)
	}
	return apperr.NotFound(msg)
}

// stdoutWriter returns the configured stdout, or os.Stdout resolved now — lazily, so
// a runner built before os.Stdout is reassigned follows the reassignment.
func (d *Runner) stdoutWriter() io.Writer {
	if d.stdout != nil {
		return d.stdout
	}
	return os.Stdout
}

// stderrWriter returns the configured stderr, or os.Stderr resolved now (see
// stdoutWriter).
func (d *Runner) stderrWriter() io.Writer {
	if d.stderr != nil {
		return d.stderr
	}
	return os.Stderr
}

// runWithEnv is the shared implementation for Run and RunWithEnv.
func (d *Runner) runWithEnv(
	ctx context.Context,
	dir string,
	extraEnv []string,
	name string,
	args ...string,
) error {
	cmd := exec.CommandContext(
		ctx,
		name,
		args...,
	)
	cmd.Dir = dir
	cmd.Stdout = d.stdoutWriter()
	cmd.Stderr = d.stderrWriter()
	cmd.Env = types.MergeEnv(os.Environ(), extraEnv)
	configureProcessGroup(cmd)

	if err := cmd.Run(); err != nil {
		cs := name
		if len(args) > 0 {
			cs = name + " " + strings.Join(args, " ")
		}
		if ctx.Err() != nil {
			return apperr.Wrap(ctx.Err(), apperr.CodeInternal, cs+" interrupted")
		}
		return apperr.Wrap(err, apperr.CodeInternal, cs+" failed")
	}
	return nil
}
