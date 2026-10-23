// Package guard builds repository checks from data: each types.GuardRow becomes one or
// more interfaces.Guard probes over a codebase. The declarative kinds are match (line
// patterns), yaml (a path query) and expr (a CEL expression over files, documents and
// rendered objects); a go row runs the Go guard the product registers under its name.
package guard

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Expander lists the rows a guard row with `each:` expands over: one map of placeholder
// values per guard to build.
type Expander func(
	ctx context.Context,
	cb interfaces.Codebase,
) ([]map[string]string, error)

// keyPath is the path key of a file, document or finding.
const keyPath = "path"

// Deps carries what the guards need besides their rows.
type Deps struct {
	// Expanders are the product's iteration domains, keyed by the name rows use in each.
	Expanders map[string]Expander

	// Go are the product's computed guards, keyed by the name a go row carries.
	Go map[string]interfaces.Probe[interfaces.Codebase]
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
		_, err := patterns(append(slices.Clone(r.Require), r.Forbid...), r.Literal)
		return err
	case types.GuardYAML:
		if r.Path == "" {
			return apperr.New(apperr.CodeInvalidInput, "a yaml guard needs a path")
		}
	case types.GuardExpr:
		_, err := compile(r.Expr)
		return err
	case types.GuardGo:
		if _, ok := deps.Go[r.Name]; !ok {
			return apperr.New(apperr.CodeInvalidInput, "no Go guard registered")
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

// Meta returns the guard's selection metadata.
func (g *guard) Meta() types.EntryMeta {
	return types.EntryMeta{
		Name:  g.name,
		Verb:  g.row.Verb,
		Group: g.row.Group,
		Tags:  g.row.Tags,
	}
}

// Run checks cb: once per expansion row (once when the row has no each), the findings of
// all expansions together deciding the verdict.
func (g *guard) Run(ctx context.Context, cb interfaces.Codebase) types.Verdict {
	expansions := []map[string]string{{}}
	if g.row.Each != "" {
		rows, err := g.deps.Expanders[g.row.Each](ctx, cb)
		if err != nil {
			return failed(err)
		}
		expansions = rows
	}
	var findings []types.Finding
	for _, vars := range expansions {
		r := expand(g.row, vars)
		fs, err := g.check(ctx, cb, &r, vars)
		if err != nil {
			return failed(err)
		}
		findings = append(findings, fs...)
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
		return exprCheck(ctx, cb, r, g.prg, vars)
	default:
		v := g.deps.Go[r.Name].Run(ctx, cb)
		if v.Status == types.StatusFail && len(v.Findings) == 0 {
			v.Findings = []types.Finding{{Message: v.Detail}}
		}
		return v.Findings, nil
	}
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

// expand replaces every `{key}` of vars in r's text fields.
func expand(r types.GuardRow, vars map[string]string) types.GuardRow {
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
	r.Require, r.Forbid = all(r.Require), all(r.Forbid)
	r.Files.Roots, r.Files.Paths, r.Files.Globs, r.Files.Exclude = all(r.Files.Roots),
		all(r.Files.Paths), all(r.Files.Globs), all(r.Files.Exclude)
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

// failed is the verdict of a guard that could not run.
func failed(err error) types.Verdict {
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
