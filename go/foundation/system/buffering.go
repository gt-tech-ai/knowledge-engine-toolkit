package system

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// Compile-time assertion: BufferingRunner must satisfy CommandRunner.
var _ CommandRunner = (*BufferingRunner)(nil)

// BufferingRunner wraps a CommandRunner, using buffered execution for Run and
// RunWithEnv. Output is replayed to stderr/stdout immediately (real-time
// developer feedback) AND accumulated for later retrieval via Captured(),
// which populates StepResult.Detail with failure diagnostics.
//
// Use NewQuietBufferingRunner when steps run concurrently to prevent
// interleaved terminal output.
type BufferingRunner struct {
	// inner is the wrapped runner that performs the buffered execution.
	inner CommandRunner

	// buf accumulates the stdout/stderr of every command for later Captured() retrieval.
	buf bytes.Buffer

	// mu guards buf against concurrent writes when steps run in parallel.
	mu sync.Mutex

	// quiet, when true, suppresses real-time terminal replay and keeps only the capture.
	quiet bool
}

// NewBufferingRunner wraps inner and replays command output to the terminal.
func NewBufferingRunner(inner CommandRunner) *BufferingRunner {
	return &BufferingRunner{inner: inner}
}

// NewQuietBufferingRunner wraps inner and captures output without terminal replay.
func NewQuietBufferingRunner(inner CommandRunner) *BufferingRunner {
	return &BufferingRunner{inner: inner, quiet: true}
}

// Run executes a command via RunBuffered, replays output to the terminal, and
// captures it for later retrieval.
func (b *BufferingRunner) Run(
	ctx context.Context,
	dir, name string,
	args ...string,
) error {
	return b.runBufferedAndReplay(ctx, dir, nil, name, args...)
}

// RunWithEnv executes a command with extra environment variables.
func (b *BufferingRunner) RunWithEnv(
	ctx context.Context,
	dir string,
	extraEnv []string,
	name string,
	args ...string,
) error {
	return b.runBufferedAndReplay(ctx, dir, extraEnv, name, args...)
}

// RunBuffered delegates to the inner runner and captures the output.
func (b *BufferingRunner) RunBuffered(
	ctx context.Context,
	dir, name string,
	args ...string,
) CmdResult {
	result := b.inner.RunBuffered(ctx, dir, name, args...)
	b.mu.Lock()
	b.buf.Write(result.Stdout)
	b.buf.Write(result.Stderr)
	b.mu.Unlock()
	return result
}

// RunBufferedWithEnv delegates to the inner runner with extra environment
// variables and captures the output.
func (b *BufferingRunner) RunBufferedWithEnv(
	ctx context.Context,
	dir string,
	extraEnv []string,
	name string,
	args ...string,
) CmdResult {
	result := b.inner.RunBufferedWithEnv(ctx, dir, extraEnv, name, args...)
	b.mu.Lock()
	b.buf.Write(result.Stdout)
	b.buf.Write(result.Stderr)
	b.mu.Unlock()
	return result
}

// RunBufferedWithInput delegates to the inner runner with extra environment
// variables and captures the output, with input fed to stdin.
func (b *BufferingRunner) RunBufferedWithInput(
	ctx context.Context,
	dir string,
	extraEnv []string,
	input []byte,
	name string,
	args ...string,
) CmdResult {
	result := b.inner.RunBufferedWithInput(ctx, dir, extraEnv, input, name, args...)
	b.mu.Lock()
	b.buf.Write(result.Stdout)
	b.buf.Write(result.Stderr)
	b.mu.Unlock()
	return result
}

// Exists delegates to the inner runner.
func (b *BufferingRunner) Exists(name string) bool { return b.inner.Exists(name) }

// RequireTool delegates to the inner runner.
func (b *BufferingRunner) RequireTool(name, installHint string) error {
	return b.inner.RequireTool(name, installHint)
}

// Captured returns all accumulated stdout/stderr from commands run through
// this runner. Safe to call after the step completes.
func (b *BufferingRunner) Captured() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buf.Bytes())
}

// runBufferedAndReplay runs a command, replays output to the terminal (unless
// quiet), and accumulates it in the internal buffer.
func (b *BufferingRunner) runBufferedAndReplay(
	ctx context.Context,
	dir string,
	extraEnv []string,
	name string,
	args ...string,
) error {
	cmdStr := name + " " + strings.Join(args, " ")
	if !b.quiet {
		fmt.Fprintf(os.Stderr, ">> %s\n", cmdStr)
	}

	if len(extraEnv) > 0 {
		err := b.inner.RunWithEnv(ctx, dir, extraEnv, name, args...)
		if err == nil && !b.quiet {
			fmt.Fprintf(os.Stderr, "ok %s\n", cmdStr)
		}
		return err
	}

	result := b.inner.RunBuffered(ctx, dir, name, args...)

	if !b.quiet {
		if len(result.Stdout) > 0 {
			_, _ = os.Stdout.Write(result.Stdout)
		}
		if len(result.Stderr) > 0 {
			_, _ = os.Stderr.Write(result.Stderr)
		}
	}

	b.mu.Lock()
	b.buf.Write(result.Stdout)
	b.buf.Write(result.Stderr)
	b.mu.Unlock()

	if result.Err != nil {
		if !b.quiet {
			fmt.Fprintf(os.Stderr, "FAIL %s\n", cmdStr)
		}
		return apperr.Wrap(result.Err, apperr.CodeInternal, cmdStr+" failed")
	}

	if !b.quiet {
		fmt.Fprintf(os.Stderr, "ok %s\n", cmdStr)
	}
	return nil
}
