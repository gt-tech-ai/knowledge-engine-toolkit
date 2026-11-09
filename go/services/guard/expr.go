package guard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"time"

	"cel.dev/cel-go/cel"
	celtypes "cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/ext"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/codebase"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
)

// exprEnvBaseOptions sizes the CEL options every expression environment starts with.
const exprEnvBaseOptions = 10

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
	return []string{textFiles, textFile, textDocs, textObjects, "vars", "data", textNow}
}

// Programs caches compiled guard expressions: one CEL environment, built on first use,
// and each expression's program, keyed by its source. A caller that builds guards more
// than once (a composite gate's phases, one gate per run) shares one through
// Deps.Programs, so each expression type-checks once per process; nil gives every
// NewFromConfig call its own. Safe for concurrent use: a CEL environment and program are.
type Programs struct {
	// env returns the guard environment, built once.
	env func() (*cel.Env, error)

	// byExpr holds each compiled expression by its source; a failed compile is not kept.
	byExpr map[string]*program

	// mu guards byExpr.
	mu sync.Mutex
}

// NewPrograms returns an empty expression cache.
func NewPrograms() *Programs {
	return &Programs{env: sync.OnceValues(newEnv), byExpr: map[string]*program{}}
}

// newEnv builds the guard environment: the CEL standard library plus optional types, the
// strings, lists, sets, encoders, bindings (cel.bind), math and regex extensions,
// decode(format, text) — the Codebase.Docs decoders ("yaml", "hcl", "alloy", "toml")
// over a string, for configuration embedded in another file — sha256(text), the
// lowercase-hex digest of a string (a copied file checked against its source's recorded
// digest), and exprVars as dyn.
func newEnv() (*cel.Env, error) {
	opts := make([]cel.EnvOption, 0, exprEnvBaseOptions+len(exprVars()))

	opts = append(
		opts,
		ext.Strings(),
		ext.Lists(),
		ext.Sets(),
		ext.Encoders(),
		cel.OptionalTypes(),
		ext.Bindings(),
		ext.Math(),
		ext.Regex(),
		decodeFunc(),
		sha256Func(),
	)

	for _, v := range exprVars() {
		opts = append(opts, cel.Variable(v, cel.DynType))
	}

	env, err := cel.NewEnv(opts...)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "cel environment")
	}

	return env, nil
}

// compile returns expr type-checked against the guard environment (newEnv), from the
// cache when an earlier call compiled it. The expression evaluates to true (pass) or
// false (fail with the row's message), or to a list of findings: strings, or maps with
// file, line and message.
func (ps *Programs) compile(expr string) (*program, error) {
	ps.mu.Lock()

	cached, ok := ps.byExpr[expr]
	ps.mu.Unlock()

	if ok {
		return cached, nil
	}

	env, err := ps.env()
	if err != nil {
		return nil, err
	}

	currentProgram, err := compileIn(env, expr)
	if err != nil {
		return nil, err
	}

	ps.mu.Lock()

	ps.byExpr[expr] = currentProgram
	ps.mu.Unlock()

	return currentProgram, nil
}

// compileIn type-checks expr in env and records the activation variables it references.
func compileIn(env *cel.Env, expr string) (*program, error) {
	ast, iss := env.Compile(expr)
	if iss.Err() != nil {
		return nil, apperr.Wrap(iss.Err(), apperr.CodeInvalidInput, textExpr)
	}

	prg, err := env.Program(ast)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, textExpr)
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
	currentCodebase interfaces.Codebase,
	guardRow *types.GuardRow,
	currentProgram *program,
	vars map[string]string,
	clock func() time.Time,
) ([]types.Finding, error) {
	act, missing, err := activation(ctx, currentCodebase, guardRow, currentProgram, vars)

	if clock == nil {
		clock = time.Now
	}

	if act != nil {
		act[textNow] = clock()
	}

	if err != nil || missing != nil {
		return missing, err
	}

	out, _, err := currentProgram.prg.ContextEval(ctx, act)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, textExpr)
	}

	return results(out, guardRow.Message)
}

// decodeFunc is the CEL function decode(format, text): text decoded by the named
// Codebase.Docs decoder, as a dyn value; a parse error fails the expression.
func decodeFunc() cel.EnvOption {
	return cel.Function("decode",
		cel.Overload("decode_string_string", []*cel.Type{cel.StringType, cel.StringType},
			cel.DynType,
			cel.BinaryBinding(func(format, text ref.Val) ref.Val {
				fileText, ok1 := format.Value().(string)
				otherText, secondOk := text.Value().(string)

				if !ok1 || !secondOk {
					return celtypes.NewErr("decode: format and text must be strings")
				}

				doc, err := codebase.Decode(fileText, "<embedded>", []byte(otherText))
				if err != nil {
					return celtypes.WrapErr(err)
				}

				return celtypes.DefaultTypeAdapter.NativeToValue(doc)
			})))
}

// sha256Func is the CEL function sha256(text): the lowercase-hex SHA-256 of text's
// UTF-8 bytes.
func sha256Func() cel.EnvOption {
	return cel.Function("sha256",
		cel.Overload("sha256_string", []*cel.Type{cel.StringType}, cel.StringType,
			cel.UnaryBinding(func(text ref.Val) ref.Val {
				t, ok := text.Value().(string)
				if !ok {
					return celtypes.NewErr("sha256: text must be a string")
				}

				sum := sha256.Sum256([]byte(t))

				return celtypes.String(hex.EncodeToString(sum[:]))
			})))
}

// activation reads what p uses.
func activation(
	ctx context.Context,
	currentCodebase interfaces.Codebase,
	guardRow *types.GuardRow,
	currentProgram *program,
	vars map[string]string,
) (map[string]any, []types.Finding, error) {
	act := baseActivation(vars, guardRow.Data)

	missing, err := addFileVars(ctx, currentCodebase, guardRow, currentProgram, act)
	if err != nil || missing != nil {
		return nil, missing, err
	}

	err = addParsedVars(ctx, currentCodebase, guardRow, currentProgram, act)
	if err != nil {
		return nil, nil, err
	}

	return act, nil, nil
}

// addParsedVars sets the `docs` and `objects` variables the program uses.
func addParsedVars(
	ctx context.Context,
	currentCodebase interfaces.Codebase,
	guardRow *types.GuardRow,
	currentProgram *program,
	act map[string]any,
) error {
	if currentProgram.uses[textDocs] {
		docs, err := decodeDocs(ctx, currentCodebase, &guardRow.Files)
		if err != nil {
			return err
		}

		act[textDocs] = docs
	}

	if currentProgram.uses[textObjects] && guardRow.Render.ChartDir != "" {
		list, err := renderedObjects(ctx, currentCodebase, guardRow)
		if err != nil {
			return err
		}

		act[textObjects] = list
	}

	return nil
}

// addFileVars sets the `files` paths and, when used, the `file` contents map.
func addFileVars(
	ctx context.Context,
	currentCodebase interfaces.Codebase,
	guardRow *types.GuardRow,
	currentProgram *program,
	act map[string]any,
) ([]types.Finding, error) {
	if !currentProgram.uses[textFiles] && !currentProgram.uses[textFile] {
		return nil, nil
	}

	currentFiles, missing, err := files(ctx, currentCodebase, guardRow)
	if err != nil || missing != nil {
		return missing, err
	}

	paths := make([]string, len(currentFiles))
	for i, f := range currentFiles {
		paths[i] = f.Path
	}

	texts, err := wantedTexts(
		ctx,
		currentCodebase,
		paths,
		currentProgram,
	)
	if err != nil {
		return nil, err
	}

	act[textFiles], act[textFile] = paths, texts

	return nil, nil
}

// wantedTexts reads the files' texts when programValue uses them.
func wantedTexts(
	ctx context.Context,
	currentCodebase interfaces.Codebase,
	paths []string,
	currentProgram *program,
) (map[string]any, error) {
	if !currentProgram.uses[textFile] {
		return map[string]any{}, nil
	}

	return fileTexts(ctx, currentCodebase, paths)
}

// fileTexts reads each path into the map the `file` variable exposes.
func fileTexts(
	ctx context.Context, cb interfaces.Codebase, paths []string,
) (map[string]any, error) {
	texts := make(map[string]any, len(paths))

	for _, text := range paths {
		fileText, err := cb.Text(ctx, text)
		if err != nil {
			return nil, err
		}

		texts[text] = map[string]any{
			keyPath: text,
			"text":  string(fileText.Bytes),
			"lines": fileText.Lines(),
		}
	}

	return texts, nil
}

// renderedObjects renders the row's chart into the list the `objects` variable exposes.
func renderedObjects(
	ctx context.Context, cb interfaces.Codebase, r *types.GuardRow,
) ([]any, error) {
	objs, err := cb.Rendered(ctx, &r.Render)
	if err != nil {
		return nil, err
	}

	list := make([]any, len(objs))
	for i := range objs {
		o := &objs[i]

		list[i] = map[string]any{
			"kind": o.Kind, "name": o.Name,
			"namespace": o.Namespace, "source": o.Source, "doc": o.Doc,
		}
	}

	return list, nil
}

// decodeDocs decodes every configuration document sel selects (Codebase.Docs: YAML by
// default; `.tf`, `.hcl`, `.tfvars`, `.alloy`, `.river` and `.json` when exts names
// them).
func decodeDocs(
	ctx context.Context,
	cb interfaces.Codebase,
	sel *types.FileSelector,
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

	value, ok := pb.(*structpb.Value)
	if !ok {
		return nil, apperr.New(apperr.CodeInternal, "expr result is not a protobuf value")
	}

	native := value.AsInterface()
	switch enabled := native.(type) {
	case bool:
		if enabled {
			return nil, nil
		}

		return []types.Finding{{Message: message}}, nil
	case []any:
		return listFindings(enabled)
	}

	return nil, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
		"expr result %T: want a bool or a list of findings", native))
}

// listFindings decodes an expression's list result.
func listFindings(list []any) ([]types.Finding, error) {
	out := make([]types.Finding, 0, len(list))

	for _, e := range list {
		f, err := finding(e)
		if err != nil {
			return nil, err
		}

		out = append(out, f)
	}

	return out, nil
}

// finding converts one list element to a finding.
func finding(value any) (types.Finding, error) {
	switch text := value.(type) {
	case string:
		return types.Finding{Message: text}, nil
	case map[string]any:
		currentFinding := types.Finding{}

		currentFinding.File = stdx.As[string](text[textFile])
		currentFinding.Message = stdx.As[string](text["message"])
		currentFinding.Rule = stdx.As[string](text["rule"])

		if n, ok := text["line"].(float64); ok {
			currentFinding.Line = int(n)
		}

		return currentFinding, nil
	}

	return types.Finding{}, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
		"expr finding %T: want a string or a map", value))
}

// baseActivation is an activation holding vars and data, with the file, document and
// object variables empty until a row's selection fills them.
func baseActivation(vars map[string]string, data any) map[string]any {
	return map[string]any{
		"vars": vars, "data": data, textFiles: []string{}, textFile: map[string]any{},
		textDocs: []any{}, textObjects: []any{},
	}
}

// forEach crosses expansions with the placeholder maps g's For returns over each one's
// vars and the row's data, in order: the map's keys are added to a copy of the expansion.
// A result that is not a list of maps of scalars is CodeInvalidInput.
func (g *guard) forEach(
	ctx context.Context,
	expansions []map[string]string,
) ([]map[string]string, error) {
	var out []map[string]string

	for _, vars := range expansions {
		rows, err := g.expandFor(ctx, vars)
		if err != nil {
			return nil, err
		}

		out = append(out, rows...)
	}

	return out, nil
}

// expandFor evaluates the `for` expression under vars and crosses vars with each item.
func (g *guard) expandFor(
	ctx context.Context, vars map[string]string,
) ([]map[string]string, error) {
	act := baseActivation(vars, g.row.Data)

	act[textNow] = time.Now()

	val, _, err := g.forPrg.prg.ContextEval(ctx, act)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "for")
	}

	pb, err := val.ConvertToNative(reflect.TypeFor[*structpb.Value]())
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "for result")
	}

	items, ok := pb.(*structpb.Value).AsInterface().([]any)
	if !ok {
		return nil, apperr.New(apperr.CodeInvalidInput, "for must return a list of maps")
	}

	rows := make([]map[string]string, 0, len(items))

	for _, item := range items {
		row, err := crossItem(vars, item)
		if err != nil {
			return nil, err
		}

		rows = append(rows, row)
	}

	return rows, nil
}

// crossItem copies vars and overlays one `for` item, which must be a map.
func crossItem(vars map[string]string, item any) (map[string]string, error) {
	lookup, ok := item.(map[string]any)
	if !ok {
		return nil, apperr.New(apperr.CodeInvalidInput, "for must return a list of maps")
	}

	row := maps.Clone(vars)
	if row == nil {
		row = map[string]string{}
	}

	for k, v := range lookup {
		row[k] = placeholder(v)
	}

	return row, nil
}

// placeholder renders a for map's value as placeholder text: a whole number without a
// fraction, anything else as fmt prints it.
func placeholder(value any) string {
	if f, ok := value.(float64); ok && f == math.Trunc(f) {
		return strconv.FormatInt(int64(f), base10)
	}

	if s, ok := value.(string); ok {
		return s
	}

	return fmt.Sprint(value)
}
