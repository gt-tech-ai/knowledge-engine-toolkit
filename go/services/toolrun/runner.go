// Package toolrun executes tool runs, the manifest rows that drive an external binary:
// it checks the tool is installed, selects targets (selectors/), fans out one
// invocation per target or runs once over all of them, skips steps whose inputs a
// previous pass already covered (the step cache), and turns output into a verdict
// (parsers/).
package toolrun

import (
	"context"
	"encoding/json"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/engine"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/stepcache"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/service"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/toolrun/parsers"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/toolrun/selectors"
)

// targetPlaceholder in a run's arguments or inputs stands for the target.
const targetPlaceholder = "{target}"

// placeholder matches a `{name}` in a run's arguments.
var placeholder = regexp.MustCompile(`\{([a-z][a-z0-9_]*)\}`)

// Config names the runner.
type Config struct {
	// Name labels the runner in decoration overrides, metrics, spans and logs; empty
	// uses "toolrun".
	Name string `yaml:"name" mapstructure:"name"`
}

// Deps carries the runner's collaborators.
type Deps struct {
	// Decorations decorates the runner as a service.
	decorate.Decorations

	// Runner runs the tools and checks they are installed.
	Runner interfaces.CommandRunner

	// Tools resolves a run's tool name to its spec; false means the toolchain does not
	// know the tool.
	Tools func(name string) (interfaces.ToolSpec, bool)

	// Versions returns a tool's version for the cache key; nil keys every tool at "".
	Versions func(name string) string

	// Cache skips steps that passed on the same inputs; nil never skips.
	Cache interfaces.StepCache

	// Git lists staged files for the staged_files selector.
	Git interfaces.GitOps

	// Root is the working-tree root the tools run under.
	Root string

	// Pool bounds a fan-out's concurrency; zero or less means one per CPU.
	Pool int
}

// runner is the ToolRunner.
type runner struct {
	// Base runs Run through the service tier as the operation "run".
	service.Base

	// deps are the runner's collaborators.
	deps Deps

	// keyed is false under the off cache, so a step computes no key: no input hashing
	// and no tool-version probe for a lookup that never hits.
	keyed bool
}

// NewFromConfig returns the ToolRunner decorated from deps as a service. A missing
// Runner or Tools is CodeInvalidInput. It does no I/O.
func NewFromConfig(cfg Config, deps Deps) (interfaces.ToolRunner, error) {
	if deps.Runner == nil || deps.Tools == nil {
		return nil, apperr.New(apperr.CodeInvalidInput,
			"toolrun: needs a command runner and a tool resolver")
	}
	keyed := !stepcache.IsOff(deps.Cache)
	if deps.Cache == nil {
		off, err := stepcache.NewFromConfig(stepcache.Config{})
		if err != nil {
			return nil, err
		}
		deps.Cache = off
	}
	name := cfg.Name
	if name == "" {
		name = "toolrun"
	}
	base, err := service.NewBase[interfaces.ToolRunner](deps.Decorations, name)
	if err != nil {
		return nil, err
	}
	return &runner{Base: base, deps: deps, keyed: keyed}, nil
}

// Run executes run over cb under opts.
func (r *runner) Run(
	ctx context.Context,
	cb interfaces.Codebase,
	run types.ToolRun,
	opts types.RunOptions,
) (types.StepResults, error) {
	return r.Exec(ctx, "run", func(ctx context.Context) (types.StepResults, error) {
		return r.run(ctx, cb, run, opts)
	})
}

// run resolves the run's tool, targets and parser, then executes it. A selector that
// picks nothing is one skipped step; a run with no selector under each: run runs once.
func (r *runner) run(
	ctx context.Context,
	cb interfaces.Codebase,
	run types.ToolRun,
	opts types.RunOptions,
) (types.StepResults, error) {
	spec, ok := r.deps.Tools(run.Tool)
	if !ok {
		return nil, apperr.New(apperr.CodeInvalidInput,
			"tool run "+run.Name+": unknown tool "+run.Tool)
	}
	if err := validate(run); err != nil {
		return nil, err
	}
	run, err := substitute(run, opts.Vars)
	if err != nil {
		return nil, err
	}
	parser, err := parsers.NewFromConfig(run.Parse)
	if err != nil {
		return nil, err
	}
	// A prefixed tool (`uv run ruff`) runs through its prefix, so the prefix is what
	// must be installed.
	if exe, _ := spec.Cmd(); !r.deps.Runner.Exists(exe) {
		return types.StepResults{missing(run, spec, opts.Strict)}, nil
	}
	targets, err := r.targets(ctx, cb, run.Targets)
	if err != nil {
		return nil, err
	}
	inv := &invocation{run: run, spec: spec, parser: parser, fix: opts.Fix}
	if len(targets) == 0 && (run.Targets.Kind != "" || run.Each == types.EachTarget) {
		return types.StepResults{{
			Name: run.Name, Group: group(run), Status: types.StatusSkip,
			Detail: []byte("no targets"),
		}}, nil
	}
	if run.Each != types.EachTarget {
		return types.StepResults{r.invoke(ctx, cb, inv, "", targets)}, nil
	}
	return engine.Pool(r.deps.Pool).FanOut(ctx, targets,
		func(ctx context.Context, target string) types.StepResult {
			return r.invoke(ctx, cb, inv, target, []string{target})
		}), nil
}

// validate rejects a run whose Each or Missing names no known value, or whose Cwd is
// neither root, target nor a directory inside the codebase root.
func validate(run types.ToolRun) error {
	ok := slices.Contains([]string{"", types.EachTarget, types.EachRun}, run.Each) &&
		validCwd(run.Cwd) &&
		slices.Contains(
			[]string{"", types.MissingSkip, types.MissingFail},
			run.Missing,
		) &&
		(run.Cwd != types.CwdTarget || run.Each == types.EachTarget)
	if ok {
		return nil
	}
	return apperr.New(
		apperr.CodeInvalidInput,
		"tool run "+run.Name+": bad each, cwd or missing "+
			"(cwd: target needs each: target; a cwd path stays inside the root)",
	)
}

// validCwd reports whether cwd is root, target, or a relative path inside the root.
func validCwd(cwd string) bool {
	switch cwd {
	case "", types.CwdRoot, types.CwdTarget:
		return true
	}
	clean := path.Clean(cwd)
	return !path.IsAbs(clean) && clean != ".." && !strings.HasPrefix(clean, "../")
}

// substitute replaces each `{name}` in run's arguments and fix arguments with
// vars[name]; a placeholder with no value is CodeInvalidInput naming the run and the
// variable. `{target}` is left for the invocation.
func substitute(run types.ToolRun, vars map[string]string) (types.ToolRun, error) {
	var missingVar string
	sub := func(args []string) []string {
		out := make([]string, len(args))
		for i, a := range args {
			out[i] = placeholder.ReplaceAllStringFunc(a, func(m string) string {
				name := m[1 : len(m)-1]
				if m == targetPlaceholder {
					return m
				}
				v, ok := vars[name]
				if !ok && missingVar == "" {
					missingVar = name
				}
				return v
			})
		}
		return out
	}
	run.Args, run.FixArgs, run.Env = sub(run.Args), sub(run.FixArgs), sub(run.Env)
	if missingVar != "" {
		return run, apperr.New(apperr.CodeInvalidInput,
			"tool run "+run.Name+": no value for {"+missingVar+"}")
	}
	return run, nil
}

// group is the gate phase run's steps report under.
func group(run types.ToolRun) string {
	if run.Group != "" {
		return run.Group
	}
	return run.Verb
}

// missing is the one step of a run whose tool is not installed; strict fails it.
func missing(run types.ToolRun, spec interfaces.ToolSpec, strict bool) types.StepResult {
	status := types.StatusSkip
	if strict || run.Missing == types.MissingFail {
		status = types.StatusFail
	}
	return types.StepResult{
		Name: run.Name, Group: group(run), Status: status,
		Error: spec.Binary() + " is not installed: " + spec.InstallHint(),
	}
}

// targets selects the run's targets; a run with no selector has none.
func (r *runner) targets(
	ctx context.Context,
	cb interfaces.Codebase,
	spec types.TargetSpec,
) ([]string, error) {
	if spec.Kind == "" {
		return nil, nil
	}
	sel, err := selectors.NewFromConfig(spec.Kind, selectors.Deps{
		Git: r.deps.Git, Root: r.deps.Root,
	})
	if err != nil {
		return nil, err
	}
	return sel.Select(ctx, cb, spec)
}

// invocation is what every step of one run shares.
type invocation struct {
	// spec is the run's tool.
	spec interfaces.ToolSpec

	// parser turns output into a verdict.
	parser interfaces.Parser

	// run is the manifest row.
	run types.ToolRun

	// fix appends the run's fix arguments.
	fix bool
}

// invoke runs the tool once: for target under each: target (named after it), or over
// every target under each: run (target empty). A cached pass skips the tool.
func (r *runner) invoke(
	ctx context.Context,
	cb interfaces.Codebase,
	inv *invocation,
	target string,
	targets []string,
) types.StepResult {
	run := inv.run
	name := strings.TrimSpace(run.Name + " " + target)
	dir, argTargets := r.deps.Root, targets
	switch run.Cwd {
	case "", types.CwdRoot:
	case types.CwdTarget:
		dir, argTargets = r.cwdOf(ctx, cb, target)
	default:
		dir = filepath.Join(r.deps.Root, filepath.FromSlash(run.Cwd))
	}
	args := expand(run.Args, argTargets)
	if inv.fix {
		args = append(args, run.FixArgs...)
	}
	key, err := r.key(ctx, cb, run, target, args)
	if err != nil {
		return types.StepResult{
			Name: name, Group: group(run), Status: types.StatusFail,
			Error: err.Error(),
		}
	}
	if key != "" {
		if hit, err := r.deps.Cache.Hit(ctx, key); err == nil && hit {
			return types.StepResult{
				Name: name, Group: group(run), Status: types.StatusSkip,
				Detail: []byte(types.CachedDetail),
			}
		}
	}
	exe, cmdArgs := inv.spec.Cmd(args...)
	var res interfaces.CmdResult
	if len(run.Env) > 0 {
		res = r.deps.Runner.RunBufferedWithEnv(ctx, dir, run.Env, exe, cmdArgs...)
	} else {
		res = r.deps.Runner.RunBuffered(ctx, dir, exe, cmdArgs...)
	}
	v := inv.parser.Parse(res)
	for i := range v.Findings {
		v.Findings[i].Check = run.Name
	}
	if run.Advisory && v.Status == types.StatusFail {
		v.Status = types.StatusWarn
	}
	step := v.Step(name, group(run))
	step.Duration = res.Duration
	if key != "" && v.Status == types.StatusPass {
		if err := r.deps.Cache.Record(ctx, key); err != nil {
			step.Status, step.Error = types.StatusWarn, "passed; not cached: "+err.Error()
		}
	}
	return step
}

// cwdOf is where a cwd: target invocation runs and what `{target}` means there: the
// target itself as "." when it is a directory, else the file's directory and its base
// name.
func (r *runner) cwdOf(
	ctx context.Context,
	cb interfaces.Codebase,
	target string,
) (dir string, argTargets []string) {
	if _, err := cb.Text(ctx, target); err == nil {
		return filepath.Join(r.deps.Root, filepath.FromSlash(path.Dir(target))),
			[]string{path.Base(target)}
	}
	return filepath.Join(r.deps.Root, filepath.FromSlash(target)), []string{"."}
}

// expand replaces each `{target}` argument with targets, and `{target}` inside a longer
// argument with the targets joined by spaces.
func expand(args, targets []string) []string {
	out := make([]string, 0, len(args)+len(targets))
	for _, a := range args {
		switch {
		case a == targetPlaceholder:
			out = append(out, targets...)
		case strings.Contains(a, targetPlaceholder):
			out = append(out, strings.ReplaceAll(a, targetPlaceholder,
				strings.Join(targets, " ")))
		default:
			out = append(out, a)
		}
	}
	return out
}

// key is the step's cache key over its inputs' paths and contents, the tool's version,
// the run's whole definition (so an edited row misses), the target and the expanded
// arguments; empty when the run has no inputs.
func (r *runner) key(
	ctx context.Context,
	cb interfaces.Codebase,
	run types.ToolRun,
	target string,
	args []string,
) (string, error) {
	if !r.keyed || run.Inputs.IsZero() {
		return "", nil
	}
	digest, err := stepcache.Digest(ctx, cb, withTarget(run.Inputs, target))
	if err != nil {
		return "", err
	}
	version := ""
	if r.deps.Versions != nil {
		version = r.deps.Versions(run.Tool)
	}
	def, err := json.Marshal(run)
	if err != nil {
		return "", apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"tool run "+run.Name+": cache key",
		)
	}
	return stepcache.Key(digest, version,
		append([]string{string(def), target}, args...)), nil
}

// withTarget returns sel with `{target}` replaced by target in its roots, paths and
// globs.
func withTarget(sel types.FileSelector, target string) types.FileSelector {
	sub := func(in []string) []string {
		out := make([]string, len(in))
		for i, s := range in {
			out[i] = strings.ReplaceAll(s, targetPlaceholder, target)
		}
		return out
	}
	sel.Roots, sel.Paths, sel.Globs = sub(sel.Roots), sub(sel.Paths), sub(sel.Globs)
	return sel
}
