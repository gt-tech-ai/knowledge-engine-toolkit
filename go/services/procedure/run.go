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

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
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
	c := e.procs[name]
	if c == nil {
		return nil, apperr.New(apperr.CodeNotFound, "no procedure "+name)
	}
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
	for _, v := range c.proc.Vars {
		val, err := c.eval(ctx, v.Expr, vars)
		if err != nil {
			return nil, apperr.Wrap(
				err,
				apperr.CodeInvalidInput,
				"procedure "+name+" var "+v.Name,
			)
		}
		vars[v.Name] = val
	}
	out, err := e.steps(ctx, c, vars, opts)
	if err != nil {
		err = apperr.Wrap(err, apperr.Code(err), "procedure "+name)
	}
	return append(out, e.finally(ctx, c, vars, opts, out.HasFailures() || err != nil)...),
		err
}

// steps runs c's steps in order over vars, which they extend, until one fails or
// errors.
func (e *engine) steps(
	ctx context.Context,
	c *compiled,
	vars Vars,
	opts types.RunOptions,
) (types.StepResults, error) {
	var out types.StepResults
	for i := range c.proc.Steps {
		st := &c.proc.Steps[i]
		res, set, err := e.step(ctx, c, st, vars, opts)
		out = append(out, res...)
		if err != nil {
			return out, apperr.Wrap(err, apperr.Code(err), "step "+st.Name)
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
	c *compiled,
	vars Vars,
	opts types.RunOptions,
	failed bool,
) types.StepResults {
	if len(c.proc.Finally) == 0 {
		return nil
	}
	timeout := c.proc.FinallyTimeout
	if timeout <= 0 {
		timeout = DefaultFinallyTimeout
	}
	fv := maps.Clone(vars)
	fv[VarFailed] = failed
	fv[VarCanceled] = ctx.Err() != nil
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	var out types.StepResults
	for i := range c.proc.Finally {
		st := &c.proc.Finally[i]
		res, set, err := e.step(fctx, c, st, fv, opts)
		if err != nil {
			res = append(res, c.result(st.Name, types.StatusFail, err.Error()))
		}
		out = append(out, res...)
		maps.Copy(fv, set)
	}
	return out
}

// step runs st under its condition and loop, returning its results and the variables
// it sets.
func (e *engine) step(
	ctx context.Context,
	c *compiled,
	st *types.ProcStep,
	vars Vars,
	opts types.RunOptions,
) (types.StepResults, Vars, error) {
	if st.When != "" {
		ok, err := c.evalBool(ctx, st.When, vars)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			return types.StepResults{
				c.result(st.Name, types.StatusSkip, "when "+st.When),
			}, nil, nil
		}
	}
	if st.Each == "" {
		name, err := text(st.Name, vars)
		if err != nil {
			return nil, nil, err
		}
		res, set, err := e.once(ctx, c, st, name, vars, opts)
		return advised(st, res), set, err
	}
	items, err := c.evalList(ctx, st.Each, vars)
	if err != nil {
		return nil, nil, err
	}
	if st.Concurrent {
		return e.eachConcurrent(ctx, c, st, items, vars, opts)
	}
	var out types.StepResults
	set := Vars{}
	for _, item := range items {
		iv := itemVars(st, vars, item)
		name, err := itemName(st, iv, item)
		if err != nil {
			return out, nil, err
		}
		res, s, err := e.once(ctx, c, st, name, iv, opts)
		out = append(out, advised(st, res)...)
		if err != nil {
			return out, nil, err
		}
		maps.Copy(set, s)
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
	c *compiled,
	st *types.ProcStep,
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
		FanOut(ctx, slots, func(ctx context.Context, s slot) types.StepResult {
			iv := itemVars(st, vars, items[s.i])
			name, err := itemName(st, iv, items[s.i])
			if err != nil {
				errs[s.i] = err
				return types.StepResult{Status: types.StatusFail}
			}
			res[s.i], _, errs[s.i] = e.once(ctx, c, st, name, iv, opts)
			return types.StepResult{Status: types.StatusPass}
		})
	var out types.StepResults
	for i := range items {
		out = append(out, advised(st, res[i])...)
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
func itemName(st *types.ProcStep, iv Vars, item any) (string, error) {
	name, err := text(st.Name, iv)
	if err != nil {
		return "", err
	}
	if name == st.Name {
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
	c *compiled,
	st *types.ProcStep,
	name string,
	vars Vars,
	opts types.RunOptions,
) (types.StepResults, Vars, error) {
	if err := e.confirm(ctx, st, vars, opts); err != nil {
		return nil, nil, err
	}
	switch {
	case st.Fail != "":
		msg, err := text(st.Fail, vars)
		if err != nil {
			return nil, nil, err
		}
		return types.StepResults{c.result(name, types.StatusFail, msg)}, nil, nil
	case len(st.Parallel) > 0:
		return e.parallel(ctx, c, st.Parallel, vars, opts)
	case st.Call != "":
		res, err := e.Run(ctx, st.Call, vars, opts)
		return res, nil, err
	case st.Probe != nil:
		return types.StepResults{e.probe(ctx, c, st.Probe, name, vars)}, nil, nil
	case st.File != nil:
		if opts.DryRun {
			return types.StepResults{
				c.result(name, types.StatusSkip, "dry-run: file "+st.File.Op),
			}, nil, nil
		}
		return types.StepResults{e.file(c, st.File, name, vars)}, nil, nil
	case st.Step != "":
		if st.Mutating && opts.DryRun {
			return types.StepResults{
					c.result(name, types.StatusSkip, "dry-run: step "+st.Step),
				},
				nil, nil
		}
		res, set, err := e.goStep(ctx, c, st, vars, opts)
		if err != nil && !res.HasFailures() {
			res = append(res, c.result(name, types.StatusFail, err.Error()))
		}
		return c.grouped(res), set, nil
	default:
		return e.tool(ctx, c, st, name, vars, opts)
	}
}

// goStep runs st's registered Go step under the recovery decorator, so a panic is the
// step's coded INTERNAL error (logged with its stack to deps.Log) instead of unwinding
// the run past its finally steps.
func (e *engine) goStep(
	ctx context.Context,
	c *compiled,
	st *types.ProcStep,
	vars Vars,
	opts types.RunOptions,
) (res types.StepResults, set Vars, err error) {
	err = decorate.NewRecovery(e.deps.Log, "procedure", c.proc.Name).WrapOp(ctx, st.Step,
		func(ctx context.Context) error {
			var stepErr error
			res, set, stepErr = e.deps.Steps[st.Step](ctx, vars, opts)
			return stepErr
		})
	return res, set, err
}

// confirm asks st's question unless the run answers yes or is a dry run.
func (e *engine) confirm(
	ctx context.Context,
	st *types.ProcStep,
	vars Vars,
	opts types.RunOptions,
) error {
	if st.Confirm == "" || opts.Yes || opts.DryRun {
		return nil
	}
	q, err := text(st.Confirm, vars)
	if err != nil {
		return err
	}
	ok := false
	if e.deps.Prompter != nil {
		if ok, err = e.deps.Prompter.Confirm(ctx, q); err != nil {
			return err
		}
	}
	if !ok {
		return apperr.New(
			apperr.CodeCanceled,
			q+" declined (pass --yes to skip the question)",
		)
	}
	return nil
}

// parallel runs steps concurrently, each over its own copy of vars; the variables they
// set merge in step order.
func (e *engine) parallel(
	ctx context.Context,
	c *compiled,
	steps []types.ProcStep,
	vars Vars,
	opts types.RunOptions,
) (types.StepResults, Vars, error) {
	res := make([]types.StepResults, len(steps))
	sets := make([]Vars, len(steps))
	errs := make([]error, len(steps))
	var wg sync.WaitGroup
	for i := range steps {
		wg.Go(func() {
			res[i], sets[i], errs[i] = e.step(ctx, c, &steps[i], maps.Clone(vars), opts)
		})
	}
	wg.Wait()
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
	c *compiled,
	st *types.ProcStep,
	name string,
	vars Vars,
	opts types.RunOptions,
) (types.StepResults, Vars, error) {
	var spec interfaces.ToolSpec
	if st.Tool != "" {
		spec, _ = e.deps.Tools(st.Tool)
	}
	inv, err := c.invocation(ctx, st, vars, spec)
	if err != nil {
		return nil, nil, err
	}
	if !e.deps.Runner.Exists(inv.exe) {
		if inv.missing == "skip" {
			return types.StepResults{
				c.result(name, types.StatusSkip, inv.exe+" not installed"),
			}, nil, nil
		}
		msg := inv.exe + " not installed"
		if spec != nil {
			msg += ": " + spec.InstallHint()
		}
		return types.StepResults{c.result(name, types.StatusFail, msg)}, nil, nil
	}
	if st.Mutating && opts.DryRun {
		cmd := strings.Join(append([]string{inv.exe}, inv.args...), " ")
		return types.StepResults{
			c.result(name, types.StatusSkip, "dry-run: "+cmd),
		}, nil, nil
	}
	dir := filepath.Join(e.deps.Root, inv.cwd)
	var r interfaces.CmdResult
	if st.Stream {
		r.Err = e.deps.Runner.RunWithEnv(ctx, dir, inv.env, inv.exe, inv.args...)
	} else {
		r = e.deps.Runner.RunBufferedWithEnv(ctx, dir, inv.env, inv.exe, inv.args...)
	}
	if st.Capture != nil && st.Capture.As == types.CaptureExit {
		code := exitStatus(r)
		done := c.result(name, types.StatusPass, "")
		if code != 0 {
			done = c.result(name, types.StatusWarn, "exit "+strconv.Itoa(code))
		}
		return types.StepResults{done}, Vars{st.Capture.Var: code}, nil
	}
	if done := c.ran(name, r); done.Status == types.StatusFail || st.Capture == nil {
		return types.StepResults{done}, nil, nil
	}
	v, err := capture(st.Capture.As, r.Stdout)
	if err != nil {
		// Output the capture cannot read is the tool's failure, reported as its row.
		f := c.result(name, types.StatusFail, err.Error())
		f.Detail = r.Stdout
		return types.StepResults{f}, nil, nil
	}
	return types.StepResults{
		c.result(name, types.StatusPass, ""),
	}, Vars{
		st.Capture.Var: v,
	}, nil
}

// exitStatus is the status a captured command exited with: 0 when it succeeded, the
// status it reported (the buffered result's, or the exit error's) when it did not, and
// 1 when it failed without one.
func exitStatus(r interfaces.CmdResult) int {
	if r.Err == nil {
		return 0
	}
	if r.ExitCode > 0 {
		return r.ExitCode
	}
	var exit *exec.ExitError
	if apperr.As(r.Err, &exit) && exit.ExitCode() > 0 {
		return exit.ExitCode()
	}
	return 1
}

// ran is the result of a command that ran: a failure carries its output as detail.
func (c *compiled) ran(name string, r interfaces.CmdResult) types.StepResult {
	if r.Err == nil {
		return c.result(name, types.StatusPass, "")
	}
	f := c.result(name, types.StatusFail, r.Err.Error())
	f.Detail = slices.Concat(r.Stdout, r.Stderr)
	return f
}

// capture converts a tool's standard output to a variable value.
func capture(as string, out []byte) (any, error) {
	switch as {
	case types.CaptureLines:
		lines := []string{}
		for l := range bytes.SplitSeq(out, []byte("\n")) {
			if s := strings.TrimSpace(string(l)); s != "" {
				lines = append(lines, s)
			}
		}
		return lines, nil
	case types.CaptureJSON:
		var v any
		if err := json.Unmarshal(out, &v); err != nil {
			return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "capture json")
		}
		return v, nil
	default:
		return strings.TrimSpace(string(out)), nil
	}
}

// result is one step result in c's group; msg is the error or skip reason.
func (c *compiled) result(
	name string,
	status types.StepStatus,
	msg string,
) types.StepResult {
	return types.StepResult{Name: name, Group: c.group, Status: status, Error: msg}
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
	st *types.ProcStep,
	vars Vars,
	spec interfaces.ToolSpec,
) (invocation, error) {
	args, err := c.args(ctx, st.Args, vars)
	if err != nil {
		return invocation{}, err
	}
	env, err := c.args(ctx, st.Env, vars)
	if err != nil {
		return invocation{}, err
	}
	cwd, err := text(st.Cwd, vars)
	if err != nil {
		return invocation{}, err
	}
	missing, err := text(st.Missing, vars)
	if err != nil {
		return invocation{}, err
	}
	if len(st.Exec) > 0 {
		argv, err := c.args(ctx, st.Exec, vars)
		if err != nil {
			return invocation{}, err
		}
		if len(argv) == 0 || argv[0] == "" {
			return invocation{}, apperr.New(
				apperr.CodeInvalidInput,
				"exec: an empty command",
			)
		}
		return invocation{
			exe: argv[0], cwd: cwd, missing: missing, args: argv[1:], env: env,
		}, nil
	}
	exe, cmdArgs := spec.Cmd(args...)
	return invocation{exe: exe, cwd: cwd, missing: missing, args: cmdArgs, env: env}, nil
}
