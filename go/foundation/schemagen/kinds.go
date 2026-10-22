package schemagen

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Source values of a KindEntry and a Section.
const (
	// SourceGo marks an entry read from Go source.
	SourceGo = "go"

	// SourcePython marks an entry introspected from a Python service.
	SourcePython = "python"
)

// KindEntry is one config-selected implementation and its allowed Kind values, read
// from the factory's own source so the documented values cannot drift from what the
// factory accepts.
type KindEntry struct {
	// Label is the human name of the selectable tier (e.g. "Storage backend").
	Label string

	// Key is the config key that selects it (e.g. "stores.kind").
	Key string

	// Source is SourceGo or SourcePython.
	Source string

	// Values are the allowed Kind strings, in declaration order.
	Values []string
}

// GoKindRef locates a Go Kind: the package whose `func (k Kind) String() string`
// enumerates the allowed values.
type GoKindRef struct {
	// Label is the human name of the selectable tier.
	Label string `yaml:"label"`

	// Key is the config key that selects it.
	Key string `yaml:"key"`

	// PackageDir is the codebase-relative package directory declaring Kind.
	PackageDir string `yaml:"package_dir"`
}

// PythonKindRef locates a Python Kind: the StrEnum whose members enumerate the allowed
// values, imported from the service's environment.
type PythonKindRef struct {
	// Label is the human name of the selectable tier.
	Label string `yaml:"label"`

	// Key is the config key that selects it.
	Key string `yaml:"key"`

	// Dir is the codebase-relative service project directory (where `uv run`
	// resolves its environment).
	Dir string `yaml:"dir"`

	// Module is the importable module holding the enum.
	Module string `yaml:"module"`

	// Enum is the StrEnum class whose members are the allowed values.
	Enum string `yaml:"enum"`
}

// ExtractGoKinds returns one entry per ref from the string literals its package's Kind
// String method returns in its non-default switch cases. A ref whose package is absent
// or has no such method is skipped.
func ExtractGoKinds(
	ctx context.Context,
	cb interfaces.Codebase,
	refs []GoKindRef,
) ([]KindEntry, error) {
	var entries []KindEntry
	for _, ref := range refs {
		files, err := goSources(ctx, cb, ref.PackageDir)
		if err != nil {
			return nil, err
		}
		var values []string
		fset := token.NewFileSet()
		for _, f := range files {
			file, err := parseGo(ctx, cb, fset, f.Path)
			if err != nil {
				return nil, err
			}
			for _, decl := range file.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && isKindString(fn) {
					values = append(values, caseStrings(fn.Body)...)
				}
			}
		}
		if len(values) > 0 {
			entries = append(entries, KindEntry{
				Label: ref.Label, Key: ref.Key, Source: SourceGo, Values: values,
			})
		}
	}
	return entries, nil
}

// isKindString reports whether fn is `func (k Kind) String() string` (value or pointer
// receiver).
func isKindString(fn *ast.FuncDecl) bool {
	if fn.Name.Name != "String" || fn.Recv == nil || len(fn.Recv.List) != 1 ||
		fn.Body == nil {
		return false
	}
	recv := fn.Recv.List[0].Type
	if star, ok := recv.(*ast.StarExpr); ok {
		recv = star.X
	}
	ident, ok := recv.(*ast.Ident)
	return ok && ident.Name == "Kind"
}

// caseStrings returns the distinct string literals returned by the non-default cases
// of the switch statements directly in body; a nested switch is not the Kind dispatch
// and the default arm's sentinel is not a valid value.
func caseStrings(body *ast.BlockStmt) []string {
	var out []string
	seen := map[string]bool{}
	for _, stmt := range body.List {
		sw, ok := stmt.(*ast.SwitchStmt)
		if !ok {
			continue
		}
		for _, cs := range sw.Body.List {
			clause, ok := cs.(*ast.CaseClause)
			if !ok || clause.List == nil {
				continue
			}
			for _, s := range clause.Body {
				if v, ok := returnedString(s); ok && !seen[v] {
					seen[v] = true
					out = append(out, v)
				}
			}
		}
	}
	return out
}

// returnedString returns the non-empty string literal s returns, if it is one.
func returnedString(s ast.Stmt) (string, bool) {
	ret, ok := s.(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return "", false
	}
	lit, ok := ret.Results[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	return v, err == nil && v != ""
}

// ExtractPythonKinds returns one entry per ref by listing the enum's member values in
// the service's environment. A ref whose service directory is absent, or whose enum
// cannot be introspected or whose module or enum is not an identifier, is left out: the
// reference is additive.
func ExtractPythonKinds(
	ctx context.Context,
	runner interfaces.CommandRunner,
	cb interfaces.Codebase,
	root string,
	refs []PythonKindRef,
) []KindEntry {
	var entries []KindEntry
	for _, ref := range refs {
		if !hasDir(ctx, cb, ref.Dir) || ref.validate() != nil {
			continue
		}
		var values []string
		script := fmt.Sprintf(
			"import json; from %s import %s; print(json.dumps([m.value for m in %s]))",
			ref.Module, ref.Enum, ref.Enum,
		)
		if err := runPython(ctx, runner, filepath.Join(root, ref.Dir), script,
			&values); err != nil || len(values) == 0 {
			continue
		}
		entries = append(entries, KindEntry{
			Label: ref.Label, Key: ref.Key, Source: SourcePython, Values: values,
		})
	}
	return entries
}

// hasDir reports whether cb holds any file under dir.
func hasDir(ctx context.Context, cb interfaces.Codebase, dir string) bool {
	files, err := cb.Files(ctx, types.FileSelector{
		Roots: []string{dir}, IncludeGenerated: true,
	})
	return err == nil && len(files) > 0
}

// runPython runs script with `uv run python -c` in dir and decodes its JSON stdout into
// out.
func runPython(
	ctx context.Context,
	runner interfaces.CommandRunner,
	dir, script string,
	out any,
) error {
	res := runner.RunBuffered(ctx, dir, "uv", "run", "python", "-c", script)
	if res.Err != nil {
		return res.Err
	}
	if err := json.Unmarshal(res.Stdout, out); err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "decode python output")
	}
	return nil
}

// validate rejects a module or enum that is not a Python identifier: both are spliced
// into the script `python -c` runs.
func (ref PythonKindRef) validate() error {
	return pythonNames(ref.Module, ref.Enum)
}

// pythonNames returns CodeInvalidInput unless module is a dotted Python identifier and
// name a plain one, so neither can carry a statement into a `python -c` script.
func pythonNames(module, name string) error {
	for _, part := range append(strings.Split(module, "."), name) {
		if !pythonIdent(part) {
			return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
				"python name %q in %q is not an identifier", part, module+"."+name))
		}
	}
	return nil
}

// pythonIdent reports whether s is a non-empty ASCII Python identifier.
func pythonIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		letter := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !letter && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}
