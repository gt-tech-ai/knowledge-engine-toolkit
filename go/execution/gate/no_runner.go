package gate

import (
	"context"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// noRunner is the CommandRunner supplied by RunLocal. Local jobs
// (job.Base.Local) never invoke it, so every method reports a programming error rather
// than silently succeeding: it fires only if a genuine command job is composed
// into a runner-less gate by mistake.
type noRunner struct{}

// errNoRunner marks a command execution attempted on a runner-less gate.
var errNoRunner = apperr.New(
	apperr.CodeInternal,
	"command executed on a runner-less gate (RunLocal): "+
		"run a command gate with Run instead",
)

// Run always fails with errNoRunner: a command was executed on a runner-less gate.
func (noRunner) Run(
	context.Context,
	string,
	string,
	...string,
) error {
	return errNoRunner
}

// RunWithEnv always fails with errNoRunner: a command was executed on a runner-less gate.
func (noRunner) RunWithEnv(context.Context, string, []string, string, ...string) error {
	return errNoRunner
}

// RunBuffered always fails, returning a CmdResult carrying errNoRunner and exit code -1.
func (noRunner) RunBuffered(
	context.Context,
	string,
	string,
	...string,
) interfaces.CmdResult {
	return interfaces.CmdResult{Err: errNoRunner, ExitCode: -1}
}

// RunBufferedWithEnv always fails, returning a CmdResult carrying errNoRunner and exit
// code -1.
func (noRunner) RunBufferedWithEnv(
	context.Context,
	string,
	[]string,
	string,
	...string,
) interfaces.CmdResult {
	return interfaces.CmdResult{Err: errNoRunner, ExitCode: -1}
}

// Exists always reports false: no tools are resolvable on a runner-less gate.
func (noRunner) Exists(string) bool { return false }

// RequireTool always fails with errNoRunner: no tools are resolvable on a runner-less
// gate.
func (noRunner) RequireTool(_, _ string) error { return errNoRunner }

// Compile-time assertion that noRunner satisfies interfaces.CommandRunner.
var _ interfaces.CommandRunner = noRunner{}
