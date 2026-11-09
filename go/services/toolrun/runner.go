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

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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
// deps must not be nil.
func NewFromConfig(cfg Config, depsRef *Deps) (interfaces.ToolRunner, error) {
	deps := *depsRef

	if deps.Runner == nil || deps.Tools == nil {
		return nil, apperr.New(apperr.CodeInvalidInput,
			"toolrun: needs a command runner and a tool resolver")
	}

	keyed := !stepcache.IsOff(deps.Cache)
	if deps.Cache == nil {
		off, err := stepcache.NewFromConfig(&stepcache.Config{})
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
// run must not be nil.
func (r *runner) Run(
	ctx context.Context,
	cb interfaces.Codebase,
	runRef *types.ToolRun,
	opts types.RunOptions,
) (types.StepResults, error) {
	run := *runRef

	return r.Exec(ctx, "run", func(ctx context.Context) (types.StepResults, error) {
		return r.runTool(ctx, cb, &run, opts)
	})
}

// runTool resolves the step's tool, targets and parser, then executes it. A selector
// that picks nothing is one skipped step; a step with no selector under each: runs once.
func (r *runner) runTool(
	ctx context.Context,
	cb interfaces.Codebase,
	runRef *types.ToolRun,
	opts types.RunOptions,
) (types.StepResults, error) {
	run := *runRef

	spec, ok := r.deps.Tools(run.Tool)
	if !ok {
		return nil, apperr.New(apperr.CodeInvalidInput,
			"tool run "+run.Name+": unknown tool "+run.Tool)
	}

	err := validate(&run)
	if err != nil {
		return nil, err
	}

	run, err = substitute(&run, opts.Vars)
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
		return types.StepResults{missing(&run, spec, strictFlag(opts.Strict))}, nil
	}

	targets, err := r.targets(ctx, cb, &run.Targets)
	if err != nil {
		return nil, err
	}

	inv := &invocation{run: run, spec: spec, parser: parser, fix: opts.Fix}

	return r.dispatch(ctx, cb, inv, targets), nil
}

// dispatch runs the tool once over all targets, or once per target when asked.
func (r *runner) dispatch(
	ctx context.Context,
	cb interfaces.Codebase,
	inv *invocation,
	targets []string,
) types.StepResults {
	run := inv.run

	if len(targets) == 0 && (run.Targets.Kind != "" || run.Each == types.EachTarget) {
		return types.StepResults{{
			Name: run.Name, Group: group(&run), Status: types.StatusSkip,
			Detail: []byte("no targets"),
		}}
	}

	if run.Each != types.EachTarget {
		return types.StepResults{r.invoke(ctx, cb, inv, "", targets)}
	}

	return engine.Pool(r.deps.Pool).FanOut(ctx, targets,
		func(ctx context.Context, target string) types.StepResult {
			return r.invoke(ctx, cb, inv, target, []string{target})
		})
}

// validate rejects a run whose Each or Missing names no known value, or whose Cwd is
// neither root, target nor a directory inside the codebase root.
func validate(run *types.ToolRun) error {
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
func substitute(runRef *types.ToolRun, vars map[string]string) (types.ToolRun, error) {
	run := *runRef

	s := &substitution{vars: vars}

	run.Args, run.FixArgs, run.Env = s.args(
		run.Args,
	), s.args(
		run.FixArgs,
	), s.args(
		run.Env,
	)

	if s.missing != "" {
		return run, apperr.New(apperr.CodeInvalidInput,
			"tool run "+run.Name+": no value for {"+s.missing+"}")
	}

	return run, nil
}

// substitution expands {name} placeholders, remembering the first with no value.
type substitution struct {
	// vars holds the placeholder values.
	vars map[string]string

	// missing names the first placeholder with no value.
	missing string
}

// args expands each argument.
func (s *substitution) args(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = placeholder.ReplaceAllStringFunc(a, s.replace)
	}

	return out
}

// replace expands one placeholder; the target placeholder is left for later.
func (s *substitution) replace(m string) string {
	if m == targetPlaceholder {
		return m
	}

	name := m[1 : len(m)-1]

	v, ok := s.vars[name]
	if !ok && s.missing == "" {
		s.missing = name
	}

	return v
}

// group is the gate phase run's steps report under.
func group(run *types.ToolRun) string {
	if run.Group != "" {
		return run.Group
	}

	return run.Verb
}

// strictFlag names the strict option in a signature.
type strictFlag bool

// missing is the one step of a run whose tool is not installed; strict fails it.
func missing(
	run *types.ToolRun,
	spec interfaces.ToolSpec,
	strict strictFlag,
) types.StepResult {
	status := types.StatusSkip
	if bool(strict) || run.Missing == types.MissingFail {
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
	spec *types.TargetSpec,
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
	dir, argTargets := r.workDir(ctx, cb, &run, target, targets)

	args := expand(run.Args, argTargets)
	if inv.fix {
		args = append(args, run.FixArgs...)
	}

	key, err := r.key(ctx, cb, &run, target, args)
	if err != nil {
		return types.StepResult{
			Name: name, Group: group(&run), Status: types.StatusFail,
			Error: err.Error(),
		}
	}

	if r.cached(ctx, key) {
		return types.StepResult{
			Name: name, Group: group(&run), Status: types.StatusSkip,
			Detail: []byte(types.CachedDetail),
		}
	}

	res := r.execute(ctx, inv, dir, args)

	v := judged(inv.parser.Parse(&res), &run)
	step := v.Step(name, group(&run))

	step.Duration = res.Duration

	if key != "" && v.Status == types.StatusPass {
		r.recordPass(ctx, key, &step)
	}

	return step
}

// workDir resolves where the tool runs and the targets to pass it.
func (r *runner) workDir(
	ctx context.Context,
	cb interfaces.Codebase,
	run *types.ToolRun,
	target string,
	targets []string,
) (dir string, resolved []string) {
	switch run.Cwd {
	case "", types.CwdRoot:
		return r.deps.Root, targets
	case types.CwdTarget:
		return r.cwdOf(ctx, cb, target)
	default:
		return filepath.Join(r.deps.Root, filepath.FromSlash(run.Cwd)), targets
	}
}

// cached reports a recorded pass for key; a cache error counts as a miss.
func (r *runner) cached(ctx context.Context, key string) bool {
	if key == "" {
		return false
	}

	hit, err := r.deps.Cache.Hit(ctx, key)

	return err == nil && hit
}

// execute runs the tool, passing the run's environment when it has one.
func (r *runner) execute(
	ctx context.Context, inv *invocation, dir string, args []string,
) interfaces.CmdResult {
	exe, cmdArgs := inv.spec.Cmd(args...)

	if len(inv.run.Env) > 0 {
		return r.deps.Runner.RunBufferedWithEnv(ctx, dir, inv.run.Env, exe, cmdArgs...)
	}

	return r.deps.Runner.RunBuffered(ctx, dir, exe, cmdArgs...)
}

// judged names the check on each finding and softens an advisory run's failure.
func judged(v types.Verdict, run *types.ToolRun) types.Verdict {
	for i := range v.Findings {
		v.Findings[i].Check = run.Name
	}

	if run.Advisory && v.Status == types.StatusFail {
		v.Status = types.StatusWarn
	}

	return v
}

// recordPass caches a pass, downgrading the step to a warning when that fails.
func (r *runner) recordPass(ctx context.Context, key string, step *types.StepResult) {
	err := r.deps.Cache.Record(ctx, key)
	if err != nil {
		step.Status, step.Error = types.StatusWarn, "passed; not cached: "+err.Error()
	}
}

// cwdOf is where a cwd: target invocation runs and what `{target}` means there: the
// target itself as "." when it is a directory, else the file's directory and its base
// name.
func (r *runner) cwdOf(
	ctx context.Context,
	cb interfaces.Codebase,
	target string,
) (dir string, argTargets []string) {
	_, err := cb.Text(ctx, target)
	if err == nil {
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
	run *types.ToolRun,
	target string,
	args []string,
) (string, error) {
	if !r.keyed || run.Inputs.IsZero() {
		return "", nil
	}

	digest, err := stepcache.Digest(ctx, cb, new(withTarget(&run.Inputs, target)))
	if err != nil {
		return "", err
	}

	version := ""
	if r.deps.Versions != nil {
		version = r.deps.Versions(run.Tool)
	}

	def, err := json.Marshal(*run)
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
func withTarget(selRef *types.FileSelector, target string) types.FileSelector {
	sel := *selRef

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
