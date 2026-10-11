package interfaces

import (
	"context"
	"time"
)

// CommandRunner executes external CLI commands.
// The canonical definition lives here (L0) so the execution engine can depend
// on it without importing foundation. foundation/system aliases this type so
// existing consumers are unchanged.
type CommandRunner interface {
	// Run executes a command with inherited stdout/stderr.
	Run(ctx context.Context, dir, name string, args ...string) error

	// RunWithEnv executes a command with additional environment variables.
	RunWithEnv(
		ctx context.Context,
		dir string,
		extraEnv []string,
		name string,
		args ...string,
	) error

	// RunBuffered executes a command with captured stdout/stderr.
	RunBuffered(ctx context.Context, dir, name string, args ...string) CmdResult

	// RunBufferedWithEnv executes a command with additional environment
	// variables and captured stdout/stderr.
	RunBufferedWithEnv(
		ctx context.Context,
		dir string,
		extraEnv []string,
		name string,
		args ...string,
	) CmdResult

	// Exists checks whether a binary is available on PATH.
	Exists(name string) bool

	// RequireTool returns an error if the named binary is not on PATH.
	RequireTool(name, installHint string) error
}

// CmdHook receives lifecycle callbacks for each command executed by a
// CommandRunner. Implementations must not block; the callbacks fire
// synchronously from the runner goroutine.
type CmdHook interface {
	// OnStart is called immediately before a command is launched.
	// cmdStr is the full command string (name + joined args).
	OnStart(cmdStr string)

	// OnSuccess is called when a command exits with a zero exit code.
	OnSuccess(cmdStr string)

	// OnFailure is called when a command exits with a non-zero exit code.
	OnFailure(cmdStr string)
}

// CmdResult holds the captured output of a buffered command execution.
type CmdResult struct {
	// Err is the error returned by the command, or nil on success.
	Err error

	// Stdout is the captured standard output.
	Stdout []byte

	// Stderr is the captured standard error.
	Stderr []byte

	// Duration is the wall-clock time the command took to execute.
	Duration time.Duration

	// ExitCode is the process exit code: 0 on success, the process's code on a
	// non-zero exit, or -1 when the command could not be started or did not exit
	// normally. Lets callers branch on specific codes (e.g. pytest's 5 = "no
	// tests collected") without parsing Err.
	ExitCode int
}
