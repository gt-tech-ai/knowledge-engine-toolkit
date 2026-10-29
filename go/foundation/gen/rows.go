package gen

import (
	"bytes"
	"context"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"text/template"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/yamlq"
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
	case types.GenEdit:
		if _, ok := deps.Data[row.Data]; !ok || len(row.Set) == 0 {
			err = apperr.New(apperr.CodeInvalidInput,
				"an edit generator needs a set and a registered data provider")
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
	if g.row.Kind == types.GenTemplate || g.row.Kind == types.GenEdit {
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

// template renders the row over its data provider into Out (a template row's template,
// or an edit row's values set in the file there), or, under Each, one file per element
// of the data.
func (g *rowGen) template(ctx context.Context, cb interfaces.Codebase) (types.Artifacts, error) {
	data, err := g.deps.Data[g.row.Data](ctx, cb)
	if err != nil {
		return nil, err
	}
	if !g.row.Each {
		return g.renderOne(ctx, cb, g.row.Template, g.row.Out, data)
	}
	list := reflect.ValueOf(data)
	if list.Kind() != reflect.Slice {
		return nil, apperr.New(apperr.CodeInvalidInput,
			"generator "+g.row.Name+": each needs list data")
	}
	out := types.Artifacts{}
	for i := range list.Len() {
		elem := list.Index(i).Interface()
		outPath, err := execute("out of "+g.row.Name, g.row.Out, elem)
		if err != nil {
			return nil, err
		}
		if _, dup := out[string(outPath)]; dup {
			return nil, apperr.New(apperr.CodeInvalidInput,
				"generator "+g.row.Name+": two elements render "+string(outPath))
		}
		one, err := g.renderOne(ctx, cb, g.row.Template, string(outPath), elem)
		if err != nil {
			return nil, err
		}
		maps.Copy(out, one)
	}
	return out, nil
}

// renderOne renders the row over data into out: an edit row's values set in the file
// there, or the template file the row's template names (itself a template over data),
// spliced into the row's region of the existing file when it has one.
func (g *rowGen) renderOne(
	ctx context.Context, cb interfaces.Codebase, tmpl, out string, data any,
) (types.Artifacts, error) {
	if g.row.Kind == types.GenEdit {
		return g.edit(ctx, cb, out, data)
	}
	tmplPath, err := execute("template of "+g.row.Name, tmpl, data)
	if err != nil {
		return nil, err
	}
	src, err := cb.Text(ctx, string(tmplPath))
	if err != nil {
		return nil, err
	}
	rendered, err := execute(string(tmplPath), string(src.Bytes), data)
	if err != nil {
		return nil, err
	}
	if g.row.Region == (types.GenRegion{}) {
		return types.Artifacts{out: rendered}, nil
	}
	current, err := cb.Text(ctx, out)
	if err != nil {
		return nil, err
	}
	spliced, err := splice(current.Bytes, rendered, g.row.Region)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "generator "+g.row.Name+" "+out)
	}
	return types.Artifacts{out: spliced}, nil
}

// edit sets each of the row's paths, in path order, in the YAML file at out to its
// value rendered over data. A path that selects no scalar is CodeInvalidInput: the file
// must declare every value the row projects.
func (g *rowGen) edit(
	ctx context.Context, cb interfaces.Codebase, out string, data any,
) (types.Artifacts, error) {
	current, err := cb.Text(ctx, out)
	if err != nil {
		return nil, err
	}
	src := current.Bytes
	for _, p := range slices.Sorted(maps.Keys(g.row.Set)) {
		value, err := execute(g.row.Name+" "+p, g.row.Set[p], data)
		if err != nil {
			return nil, err
		}
		set := 0
		src, err = yamlq.Rewrite(src, p, func(string) (string, bool) {
			set++
			return string(value), true
		})
		if err != nil {
			return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "generator "+g.row.Name+" "+out)
		}
		if set == 0 {
			return nil, apperr.New(apperr.CodeInvalidInput,
				"generator "+g.row.Name+": "+out+" has no "+p)
		}
	}
	return types.Artifacts{out: src}, nil
}

// execute renders the text/template src, named name, over data; a missing key is an
// error rather than "<no value>".
func execute(name, src string, data any) ([]byte, error) {
	tpl, err := template.New(name).Option("missingkey=error").Parse(src)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "template "+name)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "template "+name)
	}
	return buf.Bytes(), nil
}

// splice replaces, in file, the lines from the one holding r.Begin through the one
// holding r.End (after it) with block. A missing marker is CodeInvalidInput.
func splice(file, block []byte, r types.GenRegion) ([]byte, error) {
	begin := bytes.Index(file, []byte(r.Begin))
	end := -1
	if begin >= 0 {
		end = bytes.Index(file[begin:], []byte(r.End))
	}
	if begin < 0 || end < 0 {
		return nil, apperr.New(apperr.CodeInvalidInput,
			"no region markers "+r.Begin+" … "+r.End)
	}
	start := bytes.LastIndexByte(file[:begin], '\n') + 1
	stop := begin + end + len(r.End)
	if nl := bytes.IndexByte(file[stop:], '\n'); nl >= 0 {
		stop += nl + 1
	} else {
		stop = len(file)
	}
	out := make([]byte, 0, len(file)-(stop-start)+len(block))
	out = append(out, file[:start]...)
	out = append(out, block...)
	return append(out, file[stop:]...), nil
}
