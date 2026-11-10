package procedure

import (
	"context"
	"maps"
	"runtime"
	"sync"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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
