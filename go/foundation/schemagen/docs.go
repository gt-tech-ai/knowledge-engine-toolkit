package schemagen

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/printer"
	"go/token"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Field is one documented configuration attribute.
type Field struct {
	// Name is the Go field name or the Python field key.
	Name string

	// Type is the Go type text or the JSON-schema type.
	Type string

	// Key is the mapstructure key (Go) or the field key (Python).
	Key string

	// Default is the default value text, from the Go Default<Struct> constructor or
	// the Python schema's "default"; empty when there is none.
	Default string

	// Description is the Go doc comment or the Python field description.
	Description string
}

// Section is the fields of one config struct or settings model.
type Section struct {
	// Name is the Go struct or Python settings class name.
	Name string

	// Package is the Go package declaring the struct; empty for Python.
	Package string

	// Source is SourceGo or SourcePython.
	Source string

	// Fields are the documented attributes, in declaration (Go) or key (Python) order.
	Fields []Field
}

// Title is the section heading: <package>.<Name> for Go, the class name for Python.
func (s Section) Title() string {
	if s.Package == "" {
		return s.Name
	}
	return s.Package + "." + s.Name
}

// PythonSettingsRef locates one pydantic settings class.
type PythonSettingsRef struct {
	// Dir is the codebase-relative service project directory.
	Dir string `yaml:"dir"`

	// Module is the importable module holding the class.
	Module string `yaml:"module"`

	// Class is the settings class name.
	Class string `yaml:"class"`
}

// DocsConfig is what the config reference documents and where it is written.
type DocsConfig struct {
	// Path is the reference's codebase-relative output path.
	Path string `yaml:"path"`

	// Preamble is the markdown before the tables (title, provenance, how to override).
	Preamble string `yaml:"preamble"`

	// SchemaDir is the Go config schema directory; empty documents no Go structs.
	SchemaDir string `yaml:"schema_dir"`

	// PythonPrelude are Python statements run before a settings class is imported
	// (e.g. pointing its config loader at an empty directory).
	PythonPrelude string `yaml:"python_prelude"`

	// Python are the settings classes to document.
	Python []PythonSettingsRef `yaml:"python"`

	// GoKinds are the Go Kinds to list.
	GoKinds []GoKindRef `yaml:"go_kinds"`

	// PythonKinds are the Python Kinds to list.
	PythonKinds []PythonKindRef `yaml:"python_kinds"`
}

// DocsDeps carries what introspecting Python settings needs.
type DocsDeps struct {
	// Runner runs the Python introspection.
	Runner interfaces.CommandRunner

	// Root is the working-tree root the codebase paths are relative to.
	Root string
}

// ParseGoSchema returns one section per struct under dir in cb whose fields carry
// mapstructure tags, sorted by title, each field with its type, key, doc comment and
// the value its own package's Default<Struct> constructor sets. A missing dir has no
// sections.
func ParseGoSchema(
	ctx context.Context,
	cb interfaces.Codebase,
	dir string,
) ([]Section, error) {
	if dir == "" {
		return nil, nil
	}
	files, err := goSources(ctx, cb, dir)
	if err != nil {
		return nil, err
	}
	type located struct {
		pkgDir  string
		section Section
	}
	var all []located
	defaults := map[string]map[string]map[string]string{}
	fset := token.NewFileSet()
	for _, f := range files {
		file, err := parseGo(ctx, cb, fset, f.Path)
		if err != nil {
			return nil, err
		}
		pkgDir := path.Dir(f.Path)
		if defaults[pkgDir] == nil {
			defaults[pkgDir] = map[string]map[string]string{}
		}
		secs := parseSchemaFile(fset, file, defaults[pkgDir])
		for _, s := range secs {
			all = append(all, located{pkgDir: pkgDir, section: s})
		}
	}
	sections := make([]Section, 0, len(all))
	for _, l := range all {
		for i := range l.section.Fields {
			l.section.Fields[i].Default = defaults[l.pkgDir][l.section.Name][l.section.Fields[i].Name]
		}
		sections = append(sections, l.section)
	}
	sort.SliceStable(sections, func(i, j int) bool {
		return sections[i].Title() < sections[j].Title()
	})
	return sections, nil
}

// parseSchemaFile returns a section per config struct in file and records its Default
// constructors' field values into defaults (struct → field → value text).
func parseSchemaFile(
	fset *token.FileSet,
	file *ast.File,
	defaults map[string]map[string]string,
) []Section {
	var sections []Section
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			collectDefaults(fset, fn, defaults)
			continue
		}
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				continue
			}
			if fields := configFields(fset, st); len(fields) > 0 {
				sections = append(sections, Section{
					Name: ts.Name.Name, Package: file.Name.Name,
					Source: SourceGo, Fields: fields,
				})
			}
		}
	}
	return sections
}

// collectDefaults records the keyed fields of a `func Default<X>() X` (or *X)
// constructor's returned composite literal into defaults[X].
func collectDefaults(
	fset *token.FileSet,
	fn *ast.FuncDecl,
	defaults map[string]map[string]string,
) {
	if fn.Recv != nil || fn.Body == nil || !strings.HasPrefix(fn.Name.Name, "Default") {
		return
	}
	for _, stmt := range fn.Body.List {
		ret, ok := stmt.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			continue
		}
		expr := ret.Results[0]
		if u, ok := expr.(*ast.UnaryExpr); ok && u.Op == token.AND {
			expr = u.X
		}
		lit, ok := expr.(*ast.CompositeLit)
		if !ok {
			continue
		}
		name, ok := lit.Type.(*ast.Ident)
		if !ok {
			continue
		}
		if defaults[name.Name] == nil {
			defaults[name.Name] = map[string]string{}
		}
		for _, elt := range lit.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				if key, ok := kv.Key.(*ast.Ident); ok {
					defaults[name.Name][key.Name] = source(fset, kv.Value)
				}
			}
		}
	}
}

// configFields returns a struct's mapstructure-tagged fields with their doc comments.
func configFields(fset *token.FileSet, st *ast.StructType) []Field {
	var fields []Field
	for _, field := range st.Fields.List {
		if len(field.Names) == 0 || field.Tag == nil {
			continue
		}
		key := tagValue(field.Tag.Value, "mapstructure")
		if key == "" {
			continue
		}
		desc := ""
		if field.Doc != nil {
			desc = strings.Join(strings.Fields(field.Doc.Text()), " ")
		}
		fields = append(fields, Field{
			Name: field.Names[0].Name, Type: source(fset, field.Type),
			Key: key, Description: desc,
		})
	}
	return fields
}

// tagValue returns key's value in a raw struct tag, without options after a comma.
func tagValue(rawTag, key string) string {
	_, after, ok := strings.Cut(strings.Trim(rawTag, "`"), key+`:"`)
	if !ok {
		return ""
	}
	val, _, ok := strings.Cut(after, `"`)
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(val, ",")
	return name
}

// source renders an expression back to source text.
func source(fset *token.FileSet, expr ast.Expr) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, expr); err != nil {
		return "?"
	}
	return buf.String()
}

// pythonSections introspects each settings class whose service directory cb holds.
// Under strict, a present class that cannot be introspected is an error; otherwise it
// is left out.
func pythonSections(
	ctx context.Context,
	cb interfaces.Codebase,
	cfg DocsConfig,
	deps DocsDeps,
	strict bool,
) ([]Section, error) {
	var sections []Section
	for _, ref := range cfg.Python {
		if !hasDir(ctx, cb, ref.Dir) {
			continue
		}
		prelude := ""
		if cfg.PythonPrelude != "" {
			prelude = cfg.PythonPrelude + "; "
		}
		script := fmt.Sprintf("import json,os; %sfrom %s import %s; "+
			"print(json.dumps(%s.model_json_schema()))",
			prelude, ref.Module, ref.Class, ref.Class)
		var schema map[string]any
		err := runPython(ctx, deps.Runner, filepath.Join(deps.Root, ref.Dir), script, &schema)
		switch {
		case err != nil && strict:
			return nil, apperr.Wrap(err, apperr.CodeInternal,
				"introspect python settings "+ref.Class)
		case err != nil:
			continue
		}
		sections = append(sections, pythonSection(ref.Class, schema))
	}
	return sections, nil
}

// pythonSection flattens a model_json_schema's top-level properties into a section,
// sorted by key; a property with no type is an object.
func pythonSection(class string, schema map[string]any) Section {
	sec := Section{Name: class, Source: SourcePython}
	props, _ := schema["properties"].(map[string]any)
	for _, k := range slices.Sorted(maps.Keys(props)) {
		spec, _ := props[k].(map[string]any)
		desc, _ := spec["description"].(string)
		typ, _ := spec["type"].(string)
		if typ == "" {
			typ = "object"
		}
		def := ""
		if v, ok := spec["default"]; ok && v != nil {
			def = fmt.Sprintf("%v", v)
		}
		sec.Fields = append(sec.Fields, Field{
			Name: k, Type: typ, Key: k, Default: def, Description: desc,
		})
	}
	return sec
}

// docs is the config reference Generator.
type docs struct {
	// deps runs the Python introspection.
	deps DocsDeps

	// cfg is what the reference documents.
	cfg DocsConfig
}

// NewConfigDocs returns a Generator that renders the config reference to cfg.Path: the
// preamble, a Selectable Kinds table, then one table per Go section and per Python
// section. A Python class or Kind that cannot be introspected is left out.
func NewConfigDocs(cfg DocsConfig, deps DocsDeps) interfaces.Generator {
	return &docs{cfg: cfg, deps: deps}
}

// Name identifies the generator.
func (d *docs) Name() string { return "config-docs" }

// Inputs are the schema, settings and Kind source directories.
func (d *docs) Inputs() types.FileSelector {
	roots := []string{}
	if d.cfg.SchemaDir != "" {
		roots = append(roots, d.cfg.SchemaDir)
	}
	for _, r := range d.cfg.Python {
		roots = append(roots, r.Dir)
	}
	for _, r := range d.cfg.GoKinds {
		roots = append(roots, r.PackageDir)
	}
	for _, r := range d.cfg.PythonKinds {
		roots = append(roots, r.Dir)
	}
	slices.Sort(roots)
	return types.FileSelector{Roots: slices.Compact(roots), IncludeGenerated: true}
}

// Render builds the reference from cb.
func (d *docs) Render(ctx context.Context, cb interfaces.Codebase) (types.Artifacts, error) {
	goSecs, err := ParseGoSchema(ctx, cb, d.cfg.SchemaDir)
	if err != nil {
		return nil, err
	}
	pySecs, err := pythonSections(ctx, cb, d.cfg, d.deps, false)
	if err != nil {
		return nil, err
	}
	kinds, err := ExtractGoKinds(ctx, cb, d.cfg.GoKinds)
	if err != nil {
		return nil, err
	}
	kinds = append(kinds,
		ExtractPythonKinds(ctx, d.deps.Runner, cb, d.deps.Root, d.cfg.PythonKinds)...)
	return types.Artifacts{
		d.cfg.Path: []byte(render(d.cfg.Preamble, append(goSecs, pySecs...), kinds)),
	}, nil
}

// AuditContract returns the documentation violations, sorted: a Go config field with
// no doc comment and a Python field with no description. A present Python class that
// cannot be introspected is an error, so a broken environment cannot pass the audit.
func AuditContract(
	ctx context.Context,
	cb interfaces.Codebase,
	cfg DocsConfig,
	deps DocsDeps,
) ([]string, error) {
	goSecs, err := ParseGoSchema(ctx, cb, cfg.SchemaDir)
	if err != nil {
		return nil, err
	}
	pySecs, err := pythonSections(ctx, cb, cfg, deps, true)
	if err != nil {
		return nil, err
	}
	var violations []string
	for _, sec := range slices.Concat(goSecs, pySecs) {
		missing := "missing doc comment"
		if sec.Source == SourcePython {
			missing = "missing Field(description=…)"
		}
		for _, f := range sec.Fields {
			if strings.TrimSpace(f.Description) == "" {
				violations = append(violations, sec.Name+"."+f.Name+": "+missing)
			}
		}
	}
	slices.Sort(violations)
	return violations, nil
}

// render emits the reference markdown.
func render(preamble string, sections []Section, kinds []KindEntry) string {
	var b strings.Builder
	if preamble == "" {
		preamble = "# Configuration Reference\n\n"
	}
	b.WriteString(preamble)
	renderKinds(&b, kinds)
	for _, sec := range sections {
		fmt.Fprintf(&b, "## %s (%s)\n\n", sec.Title(), sec.Source)
		b.WriteString("| Field | Type | Key | Default | Description |\n" +
			"| --- | --- | --- | --- | --- |\n")
		for _, f := range sec.Fields {
			fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | %s | %s |\n",
				cell(f.Name), cell(f.Type), cell(f.Key), codeOrEmpty(f.Default),
				cell(f.Description))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// renderKinds emits the Selectable Kinds table, sorted by label; nothing when empty.
func renderKinds(b *strings.Builder, kinds []KindEntry) {
	if len(kinds) == 0 {
		return
	}
	sorted := slices.Clone(kinds)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Label < sorted[j].Label })
	b.WriteString("## Selectable Kinds\n\n")
	b.WriteString("Each config-selected implementation and its allowed `kind` values, " +
		"extracted from the factory source (Go `Kind.String()`, Python `StrEnum`). An " +
		"empty/omitted `kind` uses the tier's default (see each tier's `Default` below).\n\n")
	b.WriteString("| Kind | Config key | Allowed values | Source |\n| --- | --- | --- | --- |\n")
	for _, k := range sorted {
		vals := make([]string, len(k.Values))
		for i, v := range k.Values {
			vals[i] = "`" + escapePipe(v) + "`"
		}
		fmt.Fprintf(b, "| %s | `%s` | %s | %s |\n",
			escapePipe(k.Label), escapePipe(k.Key), strings.Join(vals, ", "), k.Source)
	}
	b.WriteString("\n")
}

// codeOrEmpty renders a default as inline code, or nothing.
func codeOrEmpty(s string) string {
	if s == "" {
		return ""
	}
	return "`" + cell(s) + "`"
}

// cell makes s safe in one table cell: whitespace runs collapse to one space, the
// padding that leaves inside braces and a trailing comma are dropped, and `|` is
// escaped.
func cell(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.NewReplacer("{ ", "{", ", }", "}", " }", "}").Replace(s)
	return escapePipe(s)
}

// escapePipe escapes a literal `|` so it cannot split a table cell.
func escapePipe(s string) string {
	return strings.ReplaceAll(s, "|", "\\|")
}
