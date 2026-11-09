package procedure

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	keengine "github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/engine"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Run runs the procedure named name: its vars, its steps in order until one fails, then
// its Finally steps whatever happened.
func (e *engine) Run(
	ctx context.Context,
	name string,
	input map[string]any,
	opts types.RunOptions,
) (types.StepResults, error) {
	secondCompiled := e.procs[name]
	if secondCompiled == nil {
		return nil, apperr.New(apperr.CodeNotFound, "no procedure "+name)
	}

	vars := seedVars(input)

	err := secondCompiled.bindVars(ctx, name, vars)
	if err != nil {
		return nil, err
	}

	out, err := e.steps(ctx, secondCompiled, vars, opts)
	if err != nil {
		err = apperr.Wrap(err, apperr.Code(err), "procedure "+name)
	}

	return append(
			out,
			e.finally(
				ctx,
				secondCompiled,
				vars,
				opts,
				out.HasFailures() || err != nil,
			)...),
		err
}

// seedVars copies the input and defaults the platform variables.
func seedVars(input map[string]any) Vars {
	vars := Vars(maps.Clone(input))
	if vars == nil {
		vars = Vars{}
	}

	if _, ok := vars[VarOS]; !ok {
		vars[VarOS] = runtime.GOOS
	}

	if _, ok := vars[VarArch]; !ok {
		vars[VarArch] = runtime.GOARCH
	}

	return vars
}

// bindVars evaluates the procedure's declared variables into vars, in order.
func (c *compiled) bindVars(ctx context.Context, name string, vars Vars) error {
	for _, procVar := range c.proc.Vars {
		val, err := c.eval(ctx, procVar.Expr, vars)
		if err != nil {
			return apperr.Wrap(
				err,
				apperr.CodeInvalidInput,
				"procedure "+name+" var "+procVar.Name,
			)
		}

		vars[procVar.Name] = val
	}

	return nil
}

// steps runs c's steps in order over vars, which they extend, until one fails or
// errors.
func (e *engine) steps(
	ctx context.Context,
	currentCompiled *compiled,
	vars Vars,
	opts types.RunOptions,
) (types.StepResults, error) {
	var out types.StepResults

	for i := range currentCompiled.proc.Steps {
		procStep := &currentCompiled.proc.Steps[i]
		res, set, err := e.step(ctx, currentCompiled, procStep, vars, opts)

		out = append(out, res...)

		if err != nil {
			return out, apperr.Wrap(err, apperr.Code(err), "step "+procStep.Name)
		}

		maps.Copy(vars, set)

		if res.HasFailures() {
			break
		}
	}

	return out, nil
}

// finally runs c's Finally steps over the run's variables plus its outcome, on a context
// detached from the run's cancellation and bounded by the procedure's timeout; a step
// that errors is a failed row, and never stops the steps after it.
func (e *engine) finally(
	ctx context.Context,
	currentCompiled *compiled,
	vars Vars,
	opts types.RunOptions,
	failed bool,
) types.StepResults {
	if len(currentCompiled.proc.Finally) == 0 {
		return nil
	}

	timeout := currentCompiled.proc.FinallyTimeout
	if timeout <= 0 {
		timeout = DefaultFinallyTimeout
	}

	currentVars := maps.Clone(vars)

	currentVars[VarFailed] = failed
	currentVars[VarCanceled] = ctx.Err() != nil

	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)

	defer cancel()

	var out types.StepResults

	for i := range currentCompiled.proc.Finally {
		procStep := &currentCompiled.proc.Finally[i]

		res, set, err := e.step(fctx, currentCompiled, procStep, currentVars, opts)
		if err != nil {
			res = append(
				res,
				currentCompiled.result(procStep.Name, types.StatusFail, err.Error()),
			)
		}

		out = append(out, res...)

		maps.Copy(currentVars, set)
	}

	return out
}

// step runs st under its condition and loop, returning its results and the variables
// it sets.
func (e *engine) step(
	ctx context.Context,
	currentCompiled *compiled,
	procStep *types.ProcStep,
	vars Vars,
	opts types.RunOptions,
) (types.StepResults, Vars, error) {
	skipped, err := whenSkip(ctx, currentCompiled, procStep, vars)
	if err != nil || skipped != nil {
		return skipped, nil, err
	}

	if procStep.Each == "" {
		return e.single(ctx, currentCompiled, procStep, vars, opts)
	}

	items, err := currentCompiled.evalList(ctx, procStep.Each, vars)
	if err != nil {
		return nil, nil, err
	}

	if procStep.Concurrent {
		return e.eachConcurrent(ctx, currentCompiled, procStep, items, vars, opts)
	}

	return e.eachSerial(ctx, currentCompiled, procStep, items, vars, opts)
}

// whenSkip evaluates the step's guard, returning its skip row when the guard is false.
func whenSkip(
	ctx context.Context, currentCompiled *compiled, procStep *types.ProcStep, vars Vars,
) (types.StepResults, error) {
	if procStep.When == "" {
		return nil, nil
	}

	ok, err := currentCompiled.evalBool(ctx, procStep.When, vars)
	if err != nil || ok {
		return nil, err
	}

	return types.StepResults{
		currentCompiled.result(procStep.Name, types.StatusSkip, "when "+procStep.When),
	}, nil
}

// single runs a step without an each.
func (e *engine) single(
	ctx context.Context,
	currentCompiled *compiled,
	procStep *types.ProcStep,
	vars Vars,
	opts types.RunOptions,
) (types.StepResults, Vars, error) {
	name, err := text(procStep.Name, vars)
	if err != nil {
		return nil, nil, err
	}

	res, set, err := e.once(ctx, currentCompiled, procStep, name, vars, opts)

	return advised(procStep, res), set, err
}

// eachSerial runs a step for each item in order, stopping at the first failure.
func (e *engine) eachSerial(
	ctx context.Context,
	currentCompiled *compiled,
	procStep *types.ProcStep,
	items []any,
	vars Vars,
	opts types.RunOptions,
) (types.StepResults, Vars, error) {
	var out types.StepResults

	set := Vars{}

	for _, item := range items {
		currentVars := itemVars(procStep, vars, item)

		name, err := itemName(procStep, currentVars, item)
		if err != nil {
			return out, nil, err
		}

		res, secondVars, err := e.once(
			ctx,
			currentCompiled,
			procStep,
			name,
			currentVars,
			opts,
		)

		out = append(out, advised(procStep, res)...)

		if err != nil {
			return out, nil, err
		}

		maps.Copy(set, secondVars)

		if res.HasFailures() {
			break
		}
	}

	return out, set, nil
}

// eachConcurrent runs st once per item concurrently (NumCPU-capped): every item runs
// whatever the others do, the results keep the items' order, and the items' variables do
// not propagate (as in a parallel block); the first item error ends the step.
func (e *engine) eachConcurrent(
	ctx context.Context,
	currentCompiled *compiled,
	procStep *types.ProcStep,
	items []any,
	vars Vars,
	opts types.RunOptions,
) (types.StepResults, Vars, error) {
	type slot struct {
		// i is the item's index, so each run fills its own slot.
		i int
	}

	res := make([]types.StepResults, len(items))
	errs := make([]error, len(items))
	slots := make([]slot, len(items))

	for i := range items {
		slots[i] = slot{i: i}
	}

	_ = keengine.Pool(0).
		FanOut(ctx, slots, func(ctx context.Context, currentSlot slot) types.StepResult {
			currentVars := itemVars(procStep, vars, items[currentSlot.i])

			name, err := itemName(procStep, currentVars, items[currentSlot.i])
			if err != nil {
				errs[currentSlot.i] = err

				return statusOnly(types.StatusFail)
			}

			res[currentSlot.i], _, errs[currentSlot.i] = e.once(
				ctx,
				currentCompiled,
				procStep,
				name,
				currentVars,
				opts,
			)

			return statusOnly(types.StatusPass)
		})

	var out types.StepResults

	for i := range items {
		out = append(out, advised(procStep, res[i])...)
		if errs[i] != nil {
			return out, nil, errs[i]
		}
	}

	return out, Vars{}, nil
}

// itemVars is vars plus item bound under the step's As name.
func itemVars(st *types.ProcStep, vars Vars, item any) Vars {
	iv := maps.Clone(vars)

	iv[cmp(st.As, "item")] = item

	return iv
}

// itemName is the step's name for one item: its Name expanded over iv, with the item
// appended when the Name names no variable.
func itemName(procStep *types.ProcStep, iv Vars, item any) (string, error) {
	name, err := text(procStep.Name, iv)
	if err != nil {
		return "", err
	}

	if name == procStep.Name {
		name += " " + scalar(item)
	}

	return name, nil
}

// advised downgrades an advisory step's failures to warnings: the step's outcome is
// reported, and the procedure goes on.
func advised(st *types.ProcStep, res types.StepResults) types.StepResults {
	if !st.Advisory {
		return res
	}

	for i := range res {
		if res[i].Status == types.StatusFail {
			res[i].Status = types.StatusWarn
		}
	}

	return res
}

// once runs st one time, after its confirmation.
func (e *engine) once(
	ctx context.Context,
	currentCompiled *compiled,
	procStep *types.ProcStep,
	name string,
	vars Vars,
	opts types.RunOptions,
) (types.StepResults, Vars, error) {
	err := e.confirm(ctx, procStep, vars, opts)
	if err != nil {
		return nil, nil, err
	}

	switch {
	case procStep.Fail != "":
		return failStep(currentCompiled, procStep, name, vars)
	case len(procStep.Parallel) > 0:
		return e.parallel(ctx, currentCompiled, procStep.Parallel, vars, opts)
	case procStep.Call != "":
		res, err := e.Run(ctx, procStep.Call, vars, opts)

		return res, nil, err
	case procStep.Probe != nil:
		return types.StepResults{
			e.probe(ctx, currentCompiled, procStep.Probe, name, vars),
		}, nil, nil
	case procStep.File != nil:
		return e.fileStep(currentCompiled, procStep, name, vars, opts)
	case procStep.Step != "":
		return e.registeredStep(ctx, currentCompiled, procStep, name, vars, opts)
	default:
		return e.tool(ctx, currentCompiled, procStep, name, vars, opts)
	}
}

// failStep reports the step's expanded fail message as a failed row.
func failStep(
	currentCompiled *compiled, st *types.ProcStep, name string, vars Vars,
) (types.StepResults, Vars, error) {
	msg, err := text(st.Fail, vars)
	if err != nil {
		return nil, nil, err
	}

	return types.StepResults{
		currentCompiled.result(name, types.StatusFail, msg),
	}, nil, nil
}

// fileStep applies a file operation, or reports it skipped on a dry run.
func (e *engine) fileStep(
	currentCompiled *compiled,
	procStep *types.ProcStep,
	name string,
	vars Vars,
	opts types.RunOptions,
) (types.StepResults, Vars, error) {
	if opts.DryRun {
		return types.StepResults{
			currentCompiled.result(
				name,
				types.StatusSkip,
				"dry-run: file "+procStep.File.Op,
			),
		}, nil, nil
	}

	return types.StepResults{e.file(currentCompiled, procStep.File, name, vars)}, nil, nil
}

// registeredStep runs a Go step, turning an unreported error into a failed row.
func (e *engine) registeredStep(
	ctx context.Context,
	currentCompiled *compiled,
	procStep *types.ProcStep,
	name string,
	vars Vars,
	opts types.RunOptions,
) (types.StepResults, Vars, error) {
	if procStep.Mutating && opts.DryRun {
		return types.StepResults{
			currentCompiled.result(
				name,
				types.StatusSkip,
				"dry-run: step "+procStep.Step,
			),
		}, nil, nil
	}

	res, set, err := e.goStep(ctx, currentCompiled, procStep, vars, opts)
	if err != nil && !res.HasFailures() {
		res = append(res, currentCompiled.result(name, types.StatusFail, err.Error()))
	}

	return currentCompiled.grouped(res), set, nil
}

// goStep runs st's registered Go step under the recovery decorator, so a panic is the
// step's coded INTERNAL error (logged with its stack to deps.Log) instead of unwinding
// the run past its finally steps.
func (e *engine) goStep(
	ctx context.Context,
	c *compiled,
	procStep *types.ProcStep,
	vars Vars,
	opts types.RunOptions,
) (res types.StepResults, set Vars, err error) {
	err = decorate.NewRecovery(e.deps.Log, "procedure", c.proc.Name).
		WrapOp(ctx, procStep.Step,
			func(ctx context.Context) error {
				var stepErr error

				res, set, stepErr = e.deps.Steps[procStep.Step](ctx, vars, opts)

				return stepErr
			})

	return res, set, err
}

// confirm asks st's question unless the run answers yes or is a dry run.
func (e *engine) confirm(
	ctx context.Context,
	procStep *types.ProcStep,
	vars Vars,
	opts types.RunOptions,
) error {
	if procStep.Confirm == "" || opts.Yes || opts.DryRun {
		return nil
	}

	fileText, err := text(procStep.Confirm, vars)
	if err != nil {
		return err
	}

	found := false
	if e.deps.Prompter != nil {
		found, err = e.deps.Prompter.Confirm(ctx, fileText)
		if err != nil {
			return err
		}
	}

	if !found {
		return apperr.New(
			apperr.CodeCanceled,
			fileText+" declined (pass --yes to skip the question)",
		)
	}

	return nil
}

// parallel runs steps concurrently, each over its own copy of vars; the variables they
// set merge in step order.
func (e *engine) parallel(
	ctx context.Context,
	currentCompiled *compiled,
	steps []types.ProcStep,
	vars Vars,
	opts types.RunOptions,
) (types.StepResults, Vars, error) {
	res := make([]types.StepResults, len(steps))
	sets := make([]Vars, len(steps))
	errs := make([]error, len(steps))

	var group sync.WaitGroup

	for i := range steps {
		group.Go(func() {
			res[i], sets[i], errs[i] = e.step(
				ctx,
				currentCompiled,
				&steps[i],
				maps.Clone(vars),
				opts,
			)
		})
	}

	group.Wait()

	var out types.StepResults

	set := Vars{}

	for i := range steps {
		out = append(out, res[i]...)
		maps.Copy(set, sets[i])

		if errs[i] != nil {
			return out, nil, errs[i]
		}
	}

	return out, set, nil
}

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
	}
}
