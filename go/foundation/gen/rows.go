package gen

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"text/template"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// DataFunc provides a template row's data from the codebase.
type DataFunc func(ctx context.Context, cb interfaces.Codebase) (any, error)

// Deps carries what row generators run with.
type Deps struct {
	// Tools runs a GenRun row's tool.
	Tools interfaces.ToolRunner

	// Data are the template data providers, by the name rows use.
	Data map[string]DataFunc
}

// NewFromRow returns the generator row describes. An unknown kind, a run row with no
// tool runner, a template row with no template or an unknown data provider is
// CodeInvalidInput naming the row.
func NewFromRow(row types.GenRow, deps Deps) (interfaces.Generator, error) {
	var err error
	switch row.Kind {
	case types.GenRun:
		if deps.Tools == nil {
			err = apperr.New(apperr.CodeInvalidInput, "a run generator needs a tool runner")
		}
	case types.GenTemplate:
		if _, ok := deps.Data[row.Data]; !ok || row.Template == "" {
			err = apperr.New(apperr.CodeInvalidInput,
				"a template generator needs a template and a registered data provider")
		}
	default:
		err = apperr.New(apperr.CodeInvalidInput, "unknown generator kind "+row.Kind)
	}
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "generator "+row.Name)
	}
	return &rowGen{row: row, deps: deps}, nil
}

// rowGen is a generator built from a row.
type rowGen struct {
	// deps are the tool runner and data providers.
	deps Deps

	// row is the generator's row.
	row types.GenRow
}

// Name returns the row's name.
func (g *rowGen) Name() string { return g.row.Name }

// Inputs returns the row's inputs.
func (g *rowGen) Inputs() types.FileSelector { return g.row.Inputs }

// Render produces the row's outputs.
func (g *rowGen) Render(ctx context.Context, cb interfaces.Codebase) (types.Artifacts, error) {
	if g.row.Kind == types.GenTemplate {
		return g.template(ctx, cb)
	}
	return g.run(ctx, cb)
}

// run runs the tool with `{out}` set to a scratch directory and reads back every file
// it wrote there, under Out.
func (g *rowGen) run(ctx context.Context, cb interfaces.Codebase) (types.Artifacts, error) {
	dir, err := os.MkdirTemp("", "gen-"+g.row.Name+"-")
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "generator "+g.row.Name)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	res, err := g.deps.Tools.Run(ctx, cb, g.row.Run, types.RunOptions{
		Vars: map[string]string{"out": dir},
	})
	if err != nil {
		return nil, err
	}
	for _, r := range res {
		if r.Status != types.StatusPass {
			return nil, apperr.New(apperr.CodeInternal,
				"generator "+g.row.Name+": "+r.Name+": "+r.Error)
		}
	}
	out := types.Artifacts{}
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p) // #nosec G304 -- p is under the scratch dir
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		out[path.Join(g.row.Out, filepath.ToSlash(rel))] = data
		return nil
	})
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "generator "+g.row.Name+" outputs")
	}
	return out, nil
}

// template renders the row's template over its data provider into Out.
func (g *rowGen) template(ctx context.Context, cb interfaces.Codebase) (types.Artifacts, error) {
	src, err := cb.Text(ctx, g.row.Template)
	if err != nil {
		return nil, err
	}
	tpl, err := template.New(g.row.Template).Option("missingkey=error").Parse(string(src.Bytes))
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "template "+g.row.Template)
	}
	data, err := g.deps.Data[g.row.Data](ctx, cb)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "template "+g.row.Template)
	}
	return types.Artifacts{g.row.Out: buf.Bytes()}, nil
}
