package procedure

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// tool runs a tool or exec step: a missing executable skips or fails, a dry run reports
// a mutating command, a streamed command's output reaches the operator, and the output
// (or, for an exit capture, the status) is captured; output a capture cannot decode
// fails the step.
func (e *engine) tool(
	ctx context.Context,
	currentCompiled *compiled,
	procStep *types.ProcStep,
	name string,
	vars Vars,
	opts types.RunOptions,
) (types.StepResults, Vars, error) {
	var spec interfaces.ToolSpec

	if procStep.Tool != "" {
		spec, _ = e.deps.Tools(procStep.Tool)
	}

	inv, err := currentCompiled.invocation(ctx, procStep, vars, spec)
	if err != nil {
		return nil, nil, err
	}

	if !e.deps.Runner.Exists(inv.exe) {
		return types.StepResults{currentCompiled.missingTool(name, &inv, spec)}, nil, nil
	}

	if procStep.Mutating && opts.DryRun {
		cmd := strings.Join(append([]string{inv.exe}, inv.args...), " ")

		return types.StepResults{
			currentCompiled.result(name, types.StatusSkip, "dry-run: "+cmd),
		}, nil, nil
	}

	cmdResult := e.runInvocation(ctx, procStep, &inv)

	if procStep.Capture != nil && procStep.Capture.As == types.CaptureExit {
		return currentCompiled.exitOutcome(name, procStep, &cmdResult)
	}

	return currentCompiled.toolOutcome(name, procStep, &cmdResult)
}

// missingTool is the row for an executable that is not installed: a skip when the step
// allows it, else a failure with the install hint.
func (c *compiled) missingTool(
	name string, inv *invocation, spec interfaces.ToolSpec,
) types.StepResult {
	msg := inv.exe + " not installed"
	if inv.missing == "skip" {
		return c.result(name, types.StatusSkip, msg)
	}

	if spec != nil {
		msg += ": " + spec.InstallHint()
	}

	return c.result(name, types.StatusFail, msg)
}

// runInvocation runs the command, streaming its output when the step asks.
func (e *engine) runInvocation(
	ctx context.Context, st *types.ProcStep, inv *invocation,
) interfaces.CmdResult {
	dir := filepath.Join(e.deps.Root, inv.cwd)

	if st.Stream {
		return interfaces.CmdResult{
			Err:      e.deps.Runner.RunWithEnv(ctx, dir, inv.env, inv.exe, inv.args...),
			Stdout:   nil,
			Stderr:   nil,
			Duration: 0,
			ExitCode: 0,
		}
	}

	return e.deps.Runner.RunBufferedWithEnv(ctx, dir, inv.env, inv.exe, inv.args...)
}

// exitOutcome captures the exit status, warning on a non-zero one.
func (c *compiled) exitOutcome(
	name string, procStep *types.ProcStep, r *interfaces.CmdResult,
) (types.StepResults, Vars, error) {
	code := exitStatus(r)
	done := c.result(name, types.StatusPass, "")

	if code != 0 {
		done = c.result(name, types.StatusWarn, "exit "+strconv.Itoa(code))
	}

	return types.StepResults{done}, Vars{procStep.Capture.Var: code}, nil
}

// toolOutcome reports the finished command and any captured output.
func (c *compiled) toolOutcome(
	name string, procStep *types.ProcStep, cmdResult *interfaces.CmdResult,
) (types.StepResults, Vars, error) {
	done := c.ran(name, cmdResult)
	if done.Status == types.StatusFail || procStep.Capture == nil {
		return types.StepResults{done}, nil, nil
	}

	value, err := capture(procStep.Capture.As, cmdResult.Stdout)
	if err != nil {
		// Output the capture cannot read is the tool's failure, reported as its row.
		f := c.result(name, types.StatusFail, err.Error())

		f.Detail = cmdResult.Stdout

		return types.StepResults{f}, nil, nil
	}

	return types.StepResults{
		c.result(name, types.StatusPass, ""),
	}, Vars{
		procStep.Capture.Var: value,
	}, nil
}

// exitStatus is the status a captured command exited with: 0 when it succeeded, the
// status it reported (the buffered result's, or the exit error's) when it did not, and
// 1 when it failed without one.
func exitStatus(cmdResult *interfaces.CmdResult) int {
	if cmdResult.Err == nil {
		return 0
	}

	if cmdResult.ExitCode > 0 {
		return cmdResult.ExitCode
	}

	var exit *exec.ExitError

	if apperr.As(cmdResult.Err, &exit) && exit.ExitCode() > 0 {
		return exit.ExitCode()
	}

	return 1
}

// ran is the result of a command that ran: a failure carries its output as detail.
func (c *compiled) ran(name string, cmdResult *interfaces.CmdResult) types.StepResult {
	if cmdResult.Err == nil {
		return c.result(name, types.StatusPass, "")
	}

	f := c.result(name, types.StatusFail, cmdResult.Err.Error())

	f.Detail = slices.Concat(cmdResult.Stdout, cmdResult.Stderr)

	return f
}

// capture converts a tool's standard output to a variable value.
func capture(as string, out []byte) (any, error) {
	switch as {
	case types.CaptureLines:
		return captureLines(out), nil
	case types.CaptureJSON:
		return captureJSON(out)
	default:
		return strings.TrimSpace(string(out)), nil
	}
}

// captureLines splits output into its non-blank, trimmed lines.
func captureLines(out []byte) []string {
	lines := []string{}

	for l := range bytes.SplitSeq(out, []byte("\n")) {
		if s := strings.TrimSpace(string(l)); s != "" {
			lines = append(lines, s)
		}
	}

	return lines
}

// captureJSON decodes output as a JSON value.
func captureJSON(out []byte) (any, error) {
	var value any

	err := json.Unmarshal(out, &value)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "capture json")
	}

	return value, nil
}

// result is one step result in c's group; msg is the error or skip reason.
func (c *compiled) result(
	name string,
	status types.StepStatus,
	msg string,
) types.StepResult {
	return types.StepResult{
		Name:     name,
		Group:    c.group,
		Status:   status,
		Error:    msg,
		Detail:   nil,
		Children: nil,
		Duration: 0,
		Tests:    0,
		Output:   false,
	}
}

// grouped puts res in c's group where a result names none.
func (c *compiled) grouped(res types.StepResults) types.StepResults {
	for i := range res {
		if res[i].Group == "" {
			res[i].Group = c.group
		}
	}

	return res
}

// invocation is a tool or exec step's expanded command line: the executable and
// arguments the tool spec renders (or the exec argv), the extra environment, the working
// directory relative to the root and the missing-tool policy.
type invocation struct {
	// exe is the executable: the tool spec's, or an exec step's first element.
	exe string

	// cwd is the working directory, relative to the root.
	cwd string

	// missing is the expanded missing-tool policy ("skip" skips).
	missing string

	// args are the expanded arguments.
	args []string

	// env are the expanded NAME=value entries.
	env []string
}

// invocation expands st's arguments, environment, working directory and missing policy
// over vars for spec.
func (c *compiled) invocation(
	ctx context.Context,
	procStep *types.ProcStep,
	vars Vars,
	spec interfaces.ToolSpec,
) (invocation, error) {
	args, err := c.args(ctx, procStep.Args, vars)
	if err != nil {
		return invocation{}, err
	}

	env, err := c.args(ctx, procStep.Env, vars)
	if err != nil {
		return invocation{}, err
	}

	cwd, err := text(procStep.Cwd, vars)
	if err != nil {
		return invocation{}, err
	}

	missing, err := text(procStep.Missing, vars)
	if err != nil {
		return invocation{}, err
	}

	if len(procStep.Exec) == 0 {
		exe, cmdArgs := spec.Cmd(args...)

		return invocation{
			exe:     exe,
			cwd:     cwd,
			missing: missing,
			args:    cmdArgs,
			env:     env,
		}, nil
	}

	argv, err := c.execArgv(ctx, procStep.Exec, vars)
	if err != nil {
		return invocation{}, err
	}

	return invocation{
		exe:     argv[0],
		cwd:     cwd,
		missing: missing,
		args:    argv[1:],
		env:     env,
	}, nil
}

// execArgv expands an exec step's command, rejecting an empty one.
func (c *compiled) execArgv(
	ctx context.Context,
	command []string,
	vars Vars,
) ([]string, error) {
	argv, err := c.args(ctx, command, vars)
	if err != nil {
		return nil, err
	}

	if len(argv) == 0 || argv[0] == "" {
		return nil, apperr.New(apperr.CodeInvalidInput, "command: an empty command")
	}

	return argv, nil
}

// statusOnly is a step result carrying only its status, for a fan-out whose slots hold
// the real results.
func statusOnly(status types.StepStatus) types.StepResult {
	return types.StepResult{
		Status:   status,
		Name:     "",
		Group:    "",
		Error:    "",
		Detail:   nil,
		Children: nil,
		Duration: 0,
		Tests:    0,
		Output:   false,
	}
}
