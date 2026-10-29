// Package guard builds repository checks from data: each types.GuardRow becomes one or
// more interfaces.Guard probes over a codebase. The declarative kinds are match (line
// patterns), yaml (a path query) and expr (a CEL expression over files, documents and
// rendered objects); a go row runs the GoGuard the product registers under its name, with
// the expanded row, so one Go guard serves many rows.
package guard

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/engine"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/stepcache"
)

// Expander lists the rows a guard row with `each:` expands over: one map of placeholder
// values per guard to build.
type Expander func(
	ctx context.Context,
	cb interfaces.Codebase,
) ([]map[string]string, error)

// GoGuard is a product check the declarative kinds cannot express. It runs once per
// expansion of its row, with the expanded row (its selector, render spec and message) and
// the expansion's placeholders, and returns its findings; none is a pass.
type GoGuard func(
	ctx context.Context,
	cb interfaces.Codebase,
	row *types.GuardRow,
	vars map[string]string,
) ([]types.Finding, error)

// keyPath is the path key of a file, document or finding.
const keyPath = "path"

// Deps carries what the guards need besides their rows.
type Deps struct {
	// Expanders are the product's iteration domains, keyed by the name rows use in each.
	Expanders map[string]Expander

	// Go are the product's computed guards, keyed by the name a go row carries.
	Go map[string]GoGuard

	// Generators returns the generator a gen row names; an error fails the row. Nil
	// rejects every gen row when building.
	Generators func(ctx context.Context, name string) (interfaces.Generator, error)

	// Cache remembers a go row's passes on its inputs (see GuardRow.Inputs); nil never
	// hits, so every expansion runs.
	Cache interfaces.StepCache

	// Versions returns a tool's version for the cache key; nil keys every tool at "".
	Versions func(tool string) string

	// Now is the clock an expr row's `now` reads; nil is time.Now.
	Now func() time.Time

	// RenderTool names the binary the codebase's renderer shells (helm), listed among
	// the tools of every row that renders a chart; empty lists none.
	RenderTool string
}

// NewFromConfig validates rows and returns their guards. Expansion runs when a guard
// runs, so building does no I/O. An unknown kind or expander, a go row with no
// registered guard, a bad pattern or a CEL expression that does not compile is
// CodeInvalidInput naming the row.
func NewFromConfig(rows []types.GuardRow, deps Deps) ([]interfaces.Guard, error) {
	out := make([]interfaces.Guard, 0, len(rows))
	for i := range rows {
		r := rows[i]
		if r.Group == "" {
			r.Group = r.Verb
		}
		if err := validate(&r, deps); err != nil {
			return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "guard "+r.Name)
		}
		var prg *program
		if r.Kind == types.GuardExpr {
			prg, _ = compile(r.Expr) // validate compiled it
		}
		names := []string{r.Name}
		if r.Kind == types.GuardMatch && len(r.Names) > 0 {
			names = r.Names
		}
		for j, n := range names {
			g := &guard{row: r, name: n, deps: deps, prg: prg, only: -1}
			if len(names) > 1 {
				g.only = j
			}
			out = append(out, g)
		}
	}
	return out, nil
}

// validate checks r's kind and the parts that can fail before a run.
func validate(r *types.GuardRow, deps Deps) error {
	if r.Each != "" {
		if _, ok := deps.Expanders[r.Each]; !ok {
			return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
				"unknown expander %q (known: %s)", r.Each,
				strings.Join(slices.Sorted(maps.Keys(deps.Expanders)), ", ")))
		}
	}
	switch r.Kind {
	case types.GuardMatch:
		if len(r.Names) > 0 && len(r.Names) != len(r.Require) {
			return apperr.New(
				apperr.CodeInvalidInput,
				"names must pair with require",
			)
		}
		all := append(append(slices.Clone(r.Require), r.Forbid...), r.Allow...)
		_, err := patterns(all, r.Literal)
		return err
	case types.GuardYAML:
		if r.Path == "" {
			return apperr.New(apperr.CodeInvalidInput, "a yaml guard needs a path")
		}
	case types.GuardExpr:
		_, err := compile(r.Expr)
		return err
	case types.GuardGen:
		if r.Gen == "" || deps.Generators == nil {
			return apperr.New(apperr.CodeInvalidInput,
				"a gen guard needs a gen and a generator lookup")
		}
	case types.GuardGo:
		if _, ok := deps.Go[goName(r)]; !ok {
			return apperr.New(apperr.CodeInvalidInput,
				fmt.Sprintf("no Go guard %q registered", goName(r)))
		}
	default:
		return apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("unknown kind %q", r.Kind),
		)
	}
	return nil
}

// guard is one guard built from a row: the whole row, or (only ≥ 0) one Require pattern
// of a named match row.
type guard struct {
	// deps are the expanders and Go guards.
	deps Deps

	// prg is an expr row's compiled expression.
	prg *program

	// name is the guard's name before expansion.
	name string

	// row is the guard's row.
	row types.GuardRow

	// only is the Require index this guard checks, or -1 for every pattern.
	only int
}

var _ interfaces.Guard = (*guard)(nil)

// Name returns the guard's name.
func (g *guard) Name() string { return g.name }

// Meta returns the guard's selection metadata: its row's tool, plus the renderer's when
// the row renders a chart, are its tools.
func (g *guard) Meta() types.EntryMeta {
	var tools []string
	if g.row.Tool != "" {
		tools = append(tools, g.row.Tool)
	}
	if g.row.Render.ChartDir != "" && g.deps.RenderTool != "" {
		tools = append(tools, g.deps.RenderTool)
	}
	return types.EntryMeta{
		Name:  g.name,
		Verb:  g.row.Verb,
		Group: g.row.Group,
		Tags:  g.row.Tags,
		Tools: tools,
	}
}

// Run checks cb: once per expansion row (once when the row has no each), the expansions
// together (one slot each, so the row's findings keep expansion order), the findings of
// all of them deciding the verdict. A go row with inputs skips every expansion whose
// inputs passed before, and is Skip "cached" when all of them did.
func (g *guard) Run(ctx context.Context, cb interfaces.Codebase) types.Verdict {
	expansions := []map[string]string{{}}
	if g.row.Each != "" {
		rows, err := g.deps.Expanders[g.row.Each](ctx, cb)
		if err != nil {
			return failed(err)
		}
		expansions = rows
	}
	type slot struct {
		err      error
		findings []types.Finding
		cached   bool
	}
	slots := make([]slot, len(expansions))
	idx := make([]int, len(expansions))
	for i := range idx {
		idx[i] = i
	}
	engine.Pool(0).FanOut(ctx, idx, func(ctx context.Context, i int) types.StepResult {
		s := &slots[i]
		s.findings, s.cached, s.err = g.runOne(ctx, cb, expansions[i])
		return g.tick(expansions[i], s.findings, s.cached, s.err)
	})
	var findings []types.Finding
	cached := len(expansions) > 0
	for _, s := range slots {
		if s.err != nil {
			return failed(s.err)
		}
		cached = cached && s.cached
		findings = append(findings, s.findings...)
	}
	if cached {
		return types.Verdict{Status: types.StatusSkip, Detail: types.CachedDetail}
	}
	if len(findings) == 0 {
		return types.Verdict{Status: types.StatusPass}
	}
	for i := range findings {
		findings[i].Check = g.name
		if findings[i].Message == "" {
			findings[i].Message = g.row.Message
		}
	}
	return types.Verdict{
		Status:   types.StatusFail,
		Findings: findings,
		Detail:   render(findings),
	}
}

// tick is one expansion's progress result for the observer: the expanded name (the
// placeholder values appended when the name carries none), skipped when cached, failed
// with its error or finding count.
func (g *guard) tick(
	vars map[string]string,
	findings []types.Finding,
	cached bool,
	err error,
) types.StepResult {
	name := expand(&g.row, vars).Name
	if name == g.name && len(vars) > 0 {
		vals := make([]string, 0, len(vars))
		for _, k := range slices.Sorted(maps.Keys(vars)) {
			vals = append(vals, vars[k])
		}
		name += " (" + strings.Join(vals, ", ") + ")"
	}
	t := types.StepResult{Name: name, Group: g.row.Group, Status: types.StatusPass}
	switch {
	case err != nil:
		t.Status, t.Error = types.StatusFail, err.Error()
	case cached:
		t.Status, t.Detail = types.StatusSkip, []byte(types.CachedDetail)
	case len(findings) > 0:
		t.Status, t.Error = types.StatusFail, fmt.Sprintf("%d findings", len(findings))
	}
	return t
}

// runOne runs one expansion: cached reports a pass the cache already held, in which
// case nothing ran; a pass with no finding is recorded.
func (g *guard) runOne(
	ctx context.Context,
	cb interfaces.Codebase,
	vars map[string]string,
) (findings []types.Finding, cached bool, err error) {
	r := expand(&g.row, vars)
	key, err := g.key(ctx, cb, &r, vars)
	if err != nil {
		return nil, false, err
	}
	if key != "" {
		if hit, err := g.deps.Cache.Hit(ctx, key); err == nil && hit {
			return nil, true, nil
		}
	}
	findings, err = g.check(ctx, cb, &r, vars)
	if key != "" && err == nil && len(findings) == 0 {
		_ = g.deps.Cache.Record(ctx, key)
	}
	return findings, false, err
}

// check runs one expanded row.
func (g *guard) check(
	ctx context.Context,
	cb interfaces.Codebase,
	r *types.GuardRow,
	vars map[string]string,
) ([]types.Finding, error) {
	switch r.Kind {
	case types.GuardMatch:
		return matchCheck(ctx, cb, r, g.only)
	case types.GuardYAML:
		return yamlCheck(ctx, cb, r)
	case types.GuardExpr:
		return exprCheck(ctx, cb, r, g.prg, vars, g.deps.Now)
	case types.GuardGen:
		return genCheck(ctx, cb, r, g.deps.Generators)
	default:
		return g.deps.Go[goName(r)](ctx, cb, r, vars)
	}
}

// key is a go row's cache key over the expanded inputs' paths and contents, its tool's
// version, its name and its placeholders; empty when the row is not a cached go row
// (no inputs, no cache).
func (g *guard) key(
	ctx context.Context,
	cb interfaces.Codebase,
	r *types.GuardRow,
	vars map[string]string,
) (string, error) {
	if r.Kind != types.GuardGo || r.Inputs.IsZero() || g.deps.Cache == nil ||
		stepcache.IsOff(g.deps.Cache) {
		return "", nil
	}
	digest, err := stepcache.Digest(ctx, cb, r.Inputs)
	if err != nil {
		return "", err
	}
	version := ""
	if g.deps.Versions != nil {
		version = g.deps.Versions(r.Tool)
	}
	args := []string{r.Name}
	for _, k := range slices.Sorted(maps.Keys(vars)) {
		args = append(args, k+"="+vars[k])
	}
	return stepcache.Key(digest, version, args), nil
}

// goName is the Go guard a go row runs: its Func, else its Name.
func goName(r *types.GuardRow) string {
	if r.Func != "" {
		return r.Func
	}
	return r.Name
}

// files returns the files r selects, or the "no file" finding when it selects none and is
// not optional.
func files(
	ctx context.Context,
	cb interfaces.Codebase,
	r *types.GuardRow,
) ([]types.File, []types.Finding, error) {
	fs, err := cb.Files(ctx, r.Files)
	if err != nil {
		return nil, nil, err
	}
	if len(fs) == 0 && !r.Optional {
		return nil, []types.Finding{
			{Message: "no file matches " + describe(r.Files)},
		}, nil
	}
	return fs, nil, nil
}

// expand returns a copy of r with every `{key}` of vars replaced in its text fields.
func expand(row *types.GuardRow, vars map[string]string) types.GuardRow {
	r := *row
	if len(vars) == 0 {
		return r
	}
	pairs := make([]string, 0, 2*len(vars))
	for _, k := range slices.Sorted(maps.Keys(vars)) {
		pairs = append(pairs, "{"+k+"}", vars[k])
	}
	rep := strings.NewReplacer(pairs...)
	all := func(in []string) []string {
		out := make([]string, len(in))
		for i, s := range in {
			out[i] = rep.Replace(s)
		}
		return out
	}
	r.Name, r.Message, r.Path, r.Expect = rep.Replace(r.Name), rep.Replace(r.Message),
		rep.Replace(r.Path), rep.Replace(r.Expect)
	r.Gen = rep.Replace(r.Gen)
	r.Require, r.Forbid, r.Allow = all(r.Require), all(r.Forbid), all(r.Allow)
	r.Files.Roots, r.Files.Paths, r.Files.Globs, r.Files.Exclude = all(r.Files.Roots),
		all(r.Files.Paths), all(r.Files.Globs), all(r.Files.Exclude)
	r.Inputs.Roots, r.Inputs.Paths = all(r.Inputs.Roots), all(r.Inputs.Paths)
	r.Inputs.Globs, r.Inputs.Exclude = all(r.Inputs.Globs), all(r.Inputs.Exclude)
	r.Render.ChartDir = rep.Replace(r.Render.ChartDir)
	r.Render.ValuesFiles = all(r.Render.ValuesFiles)
	return r
}

// compileAll compiles patterns, naming the first that does not compile.
func compileAll(patterns []string) ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "pattern "+p)
		}
		out = append(out, re)
	}
	return out, nil
}

// failed turns a check or expansion error into a verdict: a CodeUnavailable error (the
// guard's tool is not installed) skips, as a tool-run row does; anything else fails.
func failed(err error) types.Verdict {
	if apperr.Code(err) == apperr.CodeUnavailable {
		return types.Verdict{Status: types.StatusSkip, Detail: err.Error()}
	}
	return types.Verdict{Status: types.StatusFail, Detail: err.Error()}
}

// render formats findings one per line as file:line: message.
func render(fs []types.Finding) string {
	var b strings.Builder
	for _, f := range fs {
		switch {
		case f.File != "" && f.Line > 0:
			fmt.Fprintf(&b, "%s:%d: %s\n", f.File, f.Line, f.Message)
		case f.File != "":
			fmt.Fprintf(&b, "%s: %s\n", f.File, f.Message)
		default:
			b.WriteString(f.Message + "\n")
		}
	}
	return b.String()
}

// describe names a selector in a finding.
func describe(sel types.FileSelector) string {
	parts := slices.Concat(sel.Paths, sel.Globs, sel.Roots)
	if len(parts) == 0 {
		return "the selector"
	}
	return strings.Join(parts, ", ")
}
