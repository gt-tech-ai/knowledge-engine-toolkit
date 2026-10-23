package procedure

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Run runs the procedure named name.
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
	var out types.StepResults
	for i := range c.proc.Steps {
		st := &c.proc.Steps[i]
		res, set, err := e.step(ctx, c, st, vars, opts)
		out = append(out, res...)
		if err != nil {
			return out, apperr.Wrap(
				err,
				apperr.Code(err),
				"procedure "+name+" step "+st.Name,
			)
		}
		maps.Copy(vars, set)
		if res.HasFailures() && !st.Advisory {
			break
		}
	}
	return out, nil
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
		return e.once(ctx, c, st, name, vars, opts)
	}
	items, err := c.evalList(ctx, st.Each, vars)
	if err != nil {
		return nil, nil, err
	}
	var out types.StepResults
	set := Vars{}
	for _, item := range items {
		iv := maps.Clone(vars)
		iv[cmp(st.As, "item")] = item
		name, err := text(st.Name, iv)
		if err != nil {
			return out, nil, err
		}
		if name == st.Name {
			name += " " + scalar(item)
		}
		res, s, err := e.once(ctx, c, st, name, iv, opts)
		out = append(out, res...)
		if err != nil {
			return out, nil, err
		}
		maps.Copy(set, s)
		if res.HasFailures() && !st.Advisory {
			break
		}
	}
	return out, set, nil
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
	case len(st.Parallel) > 0:
		return e.parallel(ctx, c, st.Parallel, vars, opts)
	case st.Call != "":
		res, err := e.Run(ctx, st.Call, vars, opts)
		return res, nil, err
	case st.Step != "":
		if st.Mutating && opts.DryRun {
			return types.StepResults{
					c.result(name, types.StatusSkip, "dry-run: step "+st.Step),
				},
				nil, nil
		}
		res, set, err := e.deps.Steps[st.Step](ctx, vars, opts)
		if err != nil && !res.HasFailures() {
			res = append(res, c.result(name, types.StatusFail, err.Error()))
		}
		return c.grouped(res), set, nil
	default:
		return e.tool(ctx, c, st, name, vars, opts)
	}
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

// tool runs a tool step: missing tools skip or fail, a dry run reports a mutating
// command, and the output is captured.
func (e *engine) tool(
	ctx context.Context,
	c *compiled,
	st *types.ProcStep,
	name string,
	vars Vars,
	opts types.RunOptions,
) (types.StepResults, Vars, error) {
	spec, _ := e.deps.Tools(st.Tool)
	args, err := c.args(ctx, st.Args, vars)
	if err != nil {
		return nil, nil, err
	}
	env, err := texts(st.Env, vars)
	if err != nil {
		return nil, nil, err
	}
	cwd, err := text(st.Cwd, vars)
	if err != nil {
		return nil, nil, err
	}
	exe, cmdArgs := spec.Cmd(args...)
	if !e.deps.Runner.Exists(exe) {
		if st.Missing == "skip" {
			return types.StepResults{
				c.result(name, types.StatusSkip, exe+" not installed"),
			}, nil, nil
		}
		return types.StepResults{c.result(name, types.StatusFail,
			exe+" not installed: "+spec.InstallHint())}, nil, nil
	}
	cmd := strings.Join(append([]string{exe}, cmdArgs...), " ")
	if st.Mutating && opts.DryRun {
		return types.StepResults{
			c.result(name, types.StatusSkip, "dry-run: "+cmd),
		}, nil, nil
	}
	r := e.deps.Runner.RunBufferedWithEnv(
		ctx,
		filepath.Join(e.deps.Root, cwd),
		env,
		exe,
		cmdArgs...)
	if st.Capture != nil && st.Capture.As == types.CaptureExit {
		code := 0
		if r.Err != nil {
			code = 1
		}
		return types.StepResults{c.result(name, types.StatusPass, "")},
			Vars{st.Capture.Var: code}, nil
	}
	if done := c.ran(name, r); done.Status == types.StatusFail || st.Capture == nil {
		return types.StepResults{done}, nil, nil
	}
	v, err := capture(st.Capture.As, r.Stdout)
	if err != nil {
		return nil, nil, err
	}
	return types.StepResults{
		c.result(name, types.StatusPass, ""),
	}, Vars{
		st.Capture.Var: v,
	}, nil
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
