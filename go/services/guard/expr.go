package guard

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"time"

	"cel.dev/cel-go/cel"
	celtypes "cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/ext"
	"google.golang.org/protobuf/types/known/structpb"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/codebase"
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
// file (path → {path, text, lines}), docs ({path, index, format, doc} per configuration
// document: YAML, or HCL / Alloy / TOML when the selector names their extensions),
// objects ({kind, name, namespace, source, doc} per rendered object), vars (the
// expansion's placeholders), data (the row's Data, null when it has none) and now (the
// run's time, a timestamp).
func exprVars() []string {
	return []string{"files", "file", "docs", "objects", "vars", "data", "now"}
}

// compile type-checks expr against the guard environment: the CEL standard library plus
// optional types, the strings, lists, sets, encoders, bindings (cel.bind), math and
// regex extensions, and decode(format, text) — the Codebase.Docs decoders ("yaml",
// "hcl", "alloy", "toml") over a string, for configuration embedded in another file.
// The expression evaluates to true (pass) or false (fail with the row's message), or to
// a list of findings: strings, or maps with file, line and message.
func compile(expr string) (*program, error) {
	opts := make([]cel.EnvOption, 0, 4+len(exprVars()))
	opts = append(opts, ext.Strings(), ext.Lists(), ext.Sets(), ext.Encoders(),
		cel.OptionalTypes(), ext.Bindings(), ext.Math(), ext.Regex(), decodeFunc())
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
	checked, err := cel.AstToCheckedExpr(ast)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "expr references")
	}
	uses := map[string]bool{}
	for _, r := range checked.GetReferenceMap() {
		if slices.Contains(exprVars(), r.GetName()) {
			uses[r.GetName()] = true
		}
	}
	return &program{prg: prg, uses: uses}, nil
}

// exprCheck evaluates p over the files, documents and rendered objects r selects, at the
// time clock reads (nil is time.Now).
func exprCheck(
	ctx context.Context,
	cb interfaces.Codebase,
	r *types.GuardRow,
	p *program,
	vars map[string]string,
	clock func() time.Time,
) ([]types.Finding, error) {
	act, missing, err := activation(ctx, cb, r, p, vars)
	if clock == nil {
		clock = time.Now
	}
	if act != nil {
		act["now"] = clock()
	}
	if err != nil || missing != nil {
		return missing, err
	}
	out, _, err := p.prg.ContextEval(ctx, act)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "expr")
	}
	return results(out, r.Message)
}

// decodeFunc is the CEL function decode(format, text): text decoded by the named
// Codebase.Docs decoder, as a dyn value; a parse error fails the expression.
func decodeFunc() cel.EnvOption {
	return cel.Function("decode",
		cel.Overload("decode_string_string", []*cel.Type{cel.StringType, cel.StringType},
			cel.DynType,
			cel.BinaryBinding(func(format, text ref.Val) ref.Val {
				f, ok1 := format.Value().(string)
				t, ok2 := text.Value().(string)
				if !ok1 || !ok2 {
					return celtypes.NewErr("decode: format and text must be strings")
				}
				doc, err := codebase.Decode(f, "<embedded>", []byte(t))
				if err != nil {
					return celtypes.WrapErr(err)
				}
				return celtypes.DefaultTypeAdapter.NativeToValue(doc)
			})))
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
		"docs": []any{}, "objects": []any{}, "data": r.Data,
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

// decodeDocs decodes every configuration document sel selects (Codebase.Docs: YAML by
// default; `.tf`, `.hcl`, `.tfvars`, `.alloy`, `.river` and `.json` when exts names
// them).
func decodeDocs(
	ctx context.Context,
	cb interfaces.Codebase,
	sel types.FileSelector,
) ([]any, error) {
	docs, err := cb.Docs(ctx, sel)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(docs))
	for _, d := range docs {
		out = append(out, map[string]any{
			keyPath: d.Path, "index": d.Index, "format": d.Format, "doc": d.Value,
		})
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
