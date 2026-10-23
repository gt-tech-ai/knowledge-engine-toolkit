package guard

import (
	"context"
	"fmt"
	"reflect"
	"regexp"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/ext"
	"google.golang.org/protobuf/types/known/structpb"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// program is a compiled expr row and the activation variables it references, so a run
// reads only what the expression uses.
type program struct {
	// prg evaluates the expression.
	prg cel.Program

	// uses reports each activation variable the expression names.
	uses map[string]bool
}

// exprVars are the activation variables an expression may name: files (selected paths),
// file (path → {path, text, lines}), docs ({path, index, doc} per YAML document),
// objects ({kind, name, namespace, source, doc} per rendered object) and vars (the
// expansion's placeholders).
func exprVars() []string { return []string{"files", "file", "docs", "objects", "vars"} }

// compile type-checks expr against the guard environment. The expression evaluates to
// true (pass) or false (fail with the row's message), or to a list of findings: strings,
// or maps with file, line and message.
func compile(expr string) (*program, error) {
	opts := make([]cel.EnvOption, 0, 4+len(exprVars()))
	opts = append(opts, ext.Strings(), ext.Lists(), ext.Sets(), ext.Encoders())
	for _, v := range exprVars() {
		opts = append(opts, cel.Variable(v, cel.DynType))
	}
	env, err := cel.NewEnv(opts...)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "cel environment")
	}
	ast, iss := env.Compile(expr)
	if iss.Err() != nil {
		return nil, apperr.Wrap(iss.Err(), apperr.CodeInvalidInput, "expr")
	}
	prg, err := env.Program(ast)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "expr")
	}
	uses := map[string]bool{}
	for _, v := range exprVars() {
		uses[v] = regexp.MustCompile(`\b` + v + `\b`).MatchString(expr)
	}
	return &program{prg: prg, uses: uses}, nil
}

// exprCheck evaluates p over the files, documents and rendered objects r selects.
func exprCheck(
	ctx context.Context,
	cb interfaces.Codebase,
	r *types.GuardRow,
	p *program,
	vars map[string]string,
) ([]types.Finding, error) {
	act, missing, err := activation(ctx, cb, r, p, vars)
	if err != nil || missing != nil {
		return missing, err
	}
	out, _, err := p.prg.ContextEval(ctx, act)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "expr")
	}
	return results(out, r.Message)
}

// activation reads what p uses.
func activation(
	ctx context.Context,
	cb interfaces.Codebase,
	r *types.GuardRow,
	p *program,
	vars map[string]string,
) (map[string]any, []types.Finding, error) {
	act := map[string]any{
		"vars": vars, "files": []string{}, "file": map[string]any{},
		"docs": []any{}, "objects": []any{},
	}
	if p.uses["files"] || p.uses["file"] {
		fs, missing, err := files(ctx, cb, r)
		if err != nil || missing != nil {
			return nil, missing, err
		}
		paths := make([]string, len(fs))
		texts := map[string]any{}
		for i, f := range fs {
			paths[i] = f.Path
			if !p.uses["file"] {
				continue
			}
			t, err := cb.Text(ctx, f.Path)
			if err != nil {
				return nil, nil, err
			}
			texts[f.Path] = map[string]any{
				keyPath: f.Path, "text": string(t.Bytes), "lines": t.Lines(),
			}
		}
		act["files"], act["file"] = paths, texts
	}
	if p.uses["docs"] {
		docs, err := decodeDocs(ctx, cb, r.Files)
		if err != nil {
			return nil, nil, err
		}
		act["docs"] = docs
	}
	if p.uses["objects"] && r.Render.ChartDir != "" {
		objs, err := cb.Rendered(ctx, r.Render)
		if err != nil {
			return nil, nil, err
		}
		list := make([]any, len(objs))
		for i := range objs {
			o := &objs[i]
			list[i] = map[string]any{
				"kind": o.Kind, "name": o.Name,
				"namespace": o.Namespace, "source": o.Source, "doc": o.Doc,
			}
		}
		act["objects"] = list
	}
	return act, nil, nil
}

// decodeDocs decodes every YAML document sel selects.
func decodeDocs(
	ctx context.Context,
	cb interfaces.Codebase,
	sel types.FileSelector,
) ([]any, error) {
	docs, err := cb.YAML().Docs(ctx, sel)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(docs))
	for _, d := range docs {
		var doc any
		if err := cb.YAML().Decode(ctx, d, &doc); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{keyPath: d.Path, "index": d.Index, "doc": doc})
	}
	return out, nil
}

// results converts an expression's value to findings.
func results(v ref.Val, message string) ([]types.Finding, error) {
	pb, err := v.ConvertToNative(reflect.TypeFor[*structpb.Value]())
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "expr result")
	}
	native := pb.(*structpb.Value).AsInterface()
	switch x := native.(type) {
	case bool:
		if x {
			return nil, nil
		}
		return []types.Finding{{Message: message}}, nil
	case []any:
		out := make([]types.Finding, 0, len(x))
		for _, e := range x {
			f, err := finding(e)
			if err != nil {
				return nil, err
			}
			out = append(out, f)
		}
		return out, nil
	}
	return nil, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
		"expr result %T: want a bool or a list of findings", native))
}

// finding converts one list element to a finding.
func finding(e any) (types.Finding, error) {
	switch x := e.(type) {
	case string:
		return types.Finding{Message: x}, nil
	case map[string]any:
		f := types.Finding{}
		f.File, _ = x["file"].(string)
		f.Message, _ = x["message"].(string)
		f.Rule, _ = x["rule"].(string)
		if n, ok := x["line"].(float64); ok {
			f.Line = int(n)
		}
		return f, nil
	}
	return types.Finding{}, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
		"expr finding %T: want a string or a map", e))
}
