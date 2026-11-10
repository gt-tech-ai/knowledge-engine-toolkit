// Package procedure runs procedures from data: ordered steps that invoke toolchain tools
// over variables, capture their output, branch and loop on CEL expressions, ask for
// confirmation, call other procedures and hand the residue to registered Go steps. A
// dry run reports every mutating step's command instead of running it.
package procedure

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"cel.dev/cel-go/cel"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Vars are a procedure run's variables.
type Vars map[string]any

// The variables every procedure sees beyond its inputs: the platform it runs on, and,
// for its Finally steps, the run's outcome.
const (
	// VarOS is the operating system the procedure runs on (runtime.GOOS: "linux",
	// "darwin", "windows"); an input of that name overrides it.
	VarOS = "os"

	// VarArch is the processor architecture (runtime.GOARCH: "amd64", "arm64"); an
	// input of that name overrides it.
	VarArch = "arch"

	// VarFailed is true when a step failed or errored.
	VarFailed = "failed"

	// VarCanceled is true when the run's context was cancelled.
	VarCanceled = "canceled"
)

// DefaultFinallyTimeout bounds a procedure's Finally steps when it sets no
// FinallyTimeout: long enough for a container teardown, short enough that an
// interrupted run still exits.
const DefaultFinallyTimeout = 30 * time.Second

// StepFunc is a registered Go step: it reads the variables and returns its results and
// the variables it sets.
type StepFunc func(
	ctx context.Context,
	vars Vars,
	opts types.RunOptions,
) (types.StepResults, Vars, error)

// Config holds the procedures.
type Config struct {
	// Procedures are the procedures, by unique name.
	Procedures []types.Procedure
}

// Deps carries what the procedures run against.
type Deps struct {
	// Runner runs the tools and checks they are installed.
	Runner interfaces.CommandRunner

	// Tools resolves a step's tool name to its spec; false means the toolchain does not
	// know the tool.
	Tools func(name string) (interfaces.ToolSpec, bool)

	// Steps are the registered Go steps, by the name Step fields use.
	Steps map[string]StepFunc

	// Prompter asks Confirm questions; nil refuses every confirmation not answered by
	// RunOptions.Yes.
	Prompter interfaces.Prompter

	// Log receives a recovered Go step panic with its stack; nil logs nothing (the
	// step's failed row still reports it).
	Log interfaces.Logger

	// HTTP sends an HTTP probe's request; nil is http.DefaultClient (each attempt is
	// bounded by the probe's context).
	HTTP interfaces.HTTPDoer

	// Dialer opens a TCP probe's connection; nil is a zero net.Dialer.
	Dialer interfaces.Dialer

	// FS applies a file step's operation; nil is the operating system's filesystem.
	FS interfaces.FS

	// Root is the working-tree root a step's Cwd and a file step's paths are relative
	// to.
	Root string
}

// engine is the ProcedureRunner.
type engine struct {
	// procs are the compiled procedures by name.
	procs map[string]*compiled

	// deps are the runner, tools, steps and prompter.
	deps Deps
}

var _ interfaces.ProcedureRunner = (*engine)(nil)

// compiled is a validated procedure and its compiled expressions.
type compiled struct {
	// progs are the procedure's CEL programs by source.
	progs map[string]cel.Program

	// group is the phase its steps report under.
	group string

	// proc is the procedure.
	proc types.Procedure
}

// NewFromConfig validates and compiles cfg's procedures. A duplicate name, a step that
// does not run exactly one thing, an unknown tool, step or called procedure, a bad
// capture mode or a CEL expression that does not compile is CodeInvalidInput naming the
// procedure and step; the Finally steps are checked the same way. Building does no I/O.
// deps must not be nil.
func NewFromConfig(cfg Config, deps *Deps) (interfaces.ProcedureRunner, error) {
	currentEngine := &engine{deps: *deps, procs: map[string]*compiled{}}

	for i := range cfg.Procedures {
		procedure := cfg.Procedures[i]
		if _, dup := currentEngine.procs[procedure.Name]; dup {
			return nil, apperr.New(
				apperr.CodeInvalidInput,
				"duplicate procedure "+procedure.Name,
			)
		}

		currentEngine.procs[procedure.Name] = &compiled{
			proc:  procedure,
			group: cmp(procedure.Group, procedure.Name),
			progs: nil,
		}
	}

	for _, currentCompiled := range currentEngine.procs {
		err := currentEngine.compile(currentCompiled)
		if err != nil {
			return nil, apperr.Wrap(
				err,
				apperr.CodeInvalidInput,
				"procedure "+currentCompiled.proc.Name,
			)
		}
	}

	return currentEngine, nil
}

// compile validates c's steps and compiles its expressions over the variables it can
// see: its inputs, vars, captures, the variables its Go steps declare, loop items, the
// platform variables and, for the Finally steps, the outcome variables.
func (e *engine) compile(currentCompiled *compiled) error {
	names := slices.Clone(currentCompiled.proc.Inputs)
	for _, v := range currentCompiled.proc.Vars {
		names = append(names, v.Name)
	}

	names = append(names, stepNames(currentCompiled.proc.Steps)...)
	names = append(names, stepNames(currentCompiled.proc.Finally)...)
	names = append(names, VarOS, VarArch, VarFailed, VarCanceled)

	opts := make([]cel.EnvOption, 0, len(names)+1)

	opts = append(opts, celLibs())

	for _, n := range slices.Compact(slices.Sorted(slices.Values(names))) {
		opts = append(opts, cel.Variable(n, cel.DynType))
	}

	env, err := cel.NewEnv(opts...)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "cel environment")
	}

	currentCompiled.progs = map[string]cel.Program{}
	for _, v := range currentCompiled.proc.Vars {
		opErr := currentCompiled.program(env, v.Expr)
		if opErr != nil {
			return apperr.Wrap(opErr, apperr.CodeInvalidInput, "var "+v.Name)
		}
	}

	err = e.compileSteps(env, currentCompiled, currentCompiled.proc.Steps)
	if err != nil {
		return err
	}

	return e.compileSteps(env, currentCompiled, currentCompiled.proc.Finally)
}

// compileSteps validates steps and compiles their expressions.
func (e *engine) compileSteps(
	env *cel.Env,
	currentCompiled *compiled,
	steps []types.ProcStep,
) error {
	for i := range steps {
		procStep := &steps[i]

		err := e.validate(procStep)
		if err != nil {
			return apperr.Wrap(err, apperr.CodeInvalidInput, "step "+procStep.Name)
		}

		err = compileStepExprs(env, currentCompiled, procStep)
		if err != nil {
			return apperr.Wrap(err, apperr.CodeInvalidInput, "step "+procStep.Name)
		}

		err = e.compileSteps(env, currentCompiled, procStep.Parallel)
		if err != nil {
			return err
		}
	}

	return nil
}

// compileStepExprs compiles every expression the step carries.
func compileStepExprs(env *cel.Env, c *compiled, st *types.ProcStep) error {
	for _, x := range stepExprs(st) {
		err := c.program(env, x)
		if err != nil {
			return err
		}
	}

	return nil
}

// stepExprs lists the step's when and each, plus its expression arguments.
func stepExprs(procStep *types.ProcStep) []string {
	exprs := []string{procStep.When, procStep.Each}

	var probeExec []string

	if procStep.Probe != nil {
		probeExec = procStep.Probe.Exec
	}

	for _, a := range slices.Concat(procStep.Args, procStep.Exec, procStep.Env, probeExec) {
		if x, ok := exprArg(a); ok {
			exprs = append(exprs, x)
		}
	}

	return exprs
}

// validate checks that st runs exactly one known thing and captures validly.
func (e *engine) validate(procStep *types.ProcStep) error {
	err := validShape(procStep)
	if err != nil {
		return err
	}

	switch {
	case procStep.Step != "" && e.deps.Steps[procStep.Step] == nil:
		return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"no registered step %q (registered: %s)", procStep.Step,
			strings.Join(slices.Sorted(maps.Keys(e.deps.Steps)), ", ")))
	case procStep.Call != "" && e.procs[procStep.Call] == nil:
		return apperr.New(apperr.CodeInvalidInput, "no procedure "+procStep.Call)
	case procStep.Tool != "":
		err := e.validateTool(procStep.Tool)
		if err != nil {
			return err
		}
	default:
		// No other value changes the outcome.
	}

	return validCapture(procStep)
}

// validateTool checks a toolchain is wired and knows the tool.
func (e *engine) validateTool(tool string) error {
	if e.deps.Tools == nil {
		return apperr.New(apperr.CodeInvalidInput, "no toolchain for tool "+tool)
	}

	if _, ok := e.deps.Tools(tool); !ok {
		return apperr.New(apperr.CodeInvalidInput, "unknown tool "+tool)
	}

	return nil
}

// validShape checks that st runs exactly one thing, that a probe names its kind, that
// a file step's operation is valid and that only an Each step is concurrent.
func validShape(procStep *types.ProcStep) error {
	switch {
	case kinds(procStep) != 1:
		return apperr.New(
			apperr.CodeInvalidInput,
			"a step runs exactly one of tool, exec, step, call, parallel, fail, probe, file",
		)
	case procStep.Probe != nil && procStep.Probe.HTTP == "" && procStep.Probe.TCP == "" &&
		len(procStep.Probe.Exec) == 0:
		return apperr.New(apperr.CodeInvalidInput, "a probe names http, tcp or exec")
	case procStep.Concurrent && procStep.Each == "":
		return apperr.New(apperr.CodeInvalidInput, "concurrent needs each")
	case procStep.File != nil:
		return validFile(procStep.File)
	}

	return nil
}

// kinds counts what st runs: a valid step runs exactly one thing.
func kinds(st *types.ProcStep) int {
	count := 0

	for _, set := range []bool{
		st.Tool != "", len(st.Exec) > 0, st.Step != "", st.Call != "", len(st.Parallel) > 0,
		st.Fail != "", st.Probe != nil, st.File != nil,
	} {
		if set {
			count++
		}
	}

	return count
}

// validCapture checks that only a tool step captures, in a known mode, and that a
// streamed step captures only its exit status.
func validCapture(procStep *types.ProcStep) error {
	switch {
	case procStep.Capture == nil:
		return nil
	case procStep.Tool == "" && len(procStep.Exec) == 0:
		return apperr.New(apperr.CodeInvalidInput, "only a tool or exec step captures")
	case procStep.Stream && procStep.Capture.As != types.CaptureExit:
		return apperr.New(apperr.CodeInvalidInput, "a stream step captures only exit")
	case !slices.Contains([]string{
		"", types.CaptureText, types.CaptureLines,
		types.CaptureJSON, types.CaptureExit,
	}, procStep.Capture.As):
		return apperr.New(
			apperr.CodeInvalidInput,
			"unknown capture mode "+procStep.Capture.As,
		)
	}

	return nil
}

// program compiles src into c.progs; an empty source compiles to nothing.
func (c *compiled) program(env *cel.Env, src string) error {
	if src == "" || c.progs[src] != nil {
		return nil
	}

	ast, iss := env.Compile(src)
	if iss.Err() != nil {
		return apperr.Wrap(iss.Err(), apperr.CodeInvalidInput, src)
	}

	prg, err := env.Program(ast)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInvalidInput, src)
	}

	c.progs[src] = prg

	return nil
}

// stepNames returns the variables steps bind: captures, the variables Go steps declare
// and loop items.
func stepNames(steps []types.ProcStep) []string {
	var out []string

	for i := range steps {
		procStep := &steps[i]
		if procStep.Capture != nil {
			out = append(out, procStep.Capture.Var)
		}

		out = append(out, procStep.Sets...)
		if procStep.Each != "" {
			out = append(out, cmp(procStep.As, "item"))
		}

		out = append(out, stepNames(procStep.Parallel)...)
	}

	return out
}

// cmp returns s, or def when s is empty.
func cmp(s, def string) string {
	if s == "" {
		return def
	}

	return s
}
