// Package schemagen derives documentation artifacts from Go and Python config
// sources: JSON Schema (Draft 2020-12) reflected from Go types (For, Reflect), the
// allowed values of each config-selected Kind (ExtractGoKinds, ExtractPythonKinds),
// and a markdown config reference rendered as a Generator (NewConfigDocs) with its
// documentation audit (AuditContract). Sources are read through a Codebase, so the
// outputs can be drift-checked.
package schemagen

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"reflect"
	"strings"
	"time"

	"github.com/invopop/jsonschema"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Options shapes a reflected schema.
type Options struct {
	// Comments are the descriptions by fully qualified name, "<pkgPath>.<Type>" and
	// "<pkgPath>.<Type>.<Field>" (see Comments).
	Comments map[string]string

	// Tag is the struct tag that names a property; empty means "yaml".
	Tag string

	// Root, when set, wraps the schema in a one-key document requiring Root, the
	// shape of a data file holding one section.
	Root string
}

// durationType is time.Duration, reflected as string|integer.
var durationType = reflect.TypeFor[time.Duration]()

// Reflector returns an inlining reflector: Tag names the properties, only fields
// tagged jsonschema:"required" are required, every struct forbids additional
// properties, a time.Duration accepts a duration string or an integer, and
// descriptions come from Comments.
func Reflector(opts Options) *jsonschema.Reflector {
	tag := opts.Tag
	if tag == "" {
		tag = "yaml"
	}
	return &jsonschema.Reflector{
		FieldNameTag:               tag,
		DoNotReference:             true,
		RequiredFromJSONSchemaTags: true,
		Mapper: func(t reflect.Type) *jsonschema.Schema {
			if t != durationType {
				return nil
			}
			return &jsonschema.Schema{OneOf: []*jsonschema.Schema{
				{Type: "string"}, {Type: "integer"},
			}}
		},
		LookupComment: func(t reflect.Type, name string) string {
			key := t.PkgPath() + "." + t.Name()
			if name != "" {
				key += "." + name
			}
			return opts.Comments[key]
		},
	}
}

// Reflect returns the schema of value's type under opts.
func Reflect(value any, opts Options) *jsonschema.Schema {
	r := Reflector(opts)
	if opts.Root == "" {
		return r.ReflectFromType(reflect.TypeOf(value))
	}
	tag := opts.Tag
	if tag == "" {
		tag = "yaml"
	}
	wrapper := reflect.StructOf([]reflect.StructField{{
		Name: "Section",
		Type: reflect.TypeOf(value),
		Tag:  reflect.StructTag(tag + `:"` + opts.Root + `" jsonschema:"required"`),
	}})
	return r.ReflectFromType(wrapper)
}

// For returns T's schema under opts in its committed form (see Marshal).
func For[T any](opts Options) ([]byte, error) {
	var zero T
	return Marshal(Reflect(zero, opts))
}

// AllowExtraProps lifts additionalProperties: false from s and every schema below it
// (properties, array items, map values), so a section admits keys its struct does not
// model while each modeled field keeps its type.
func AllowExtraProps(s *jsonschema.Schema) {
	if s == nil {
		return
	}
	switch {
	case s.AdditionalProperties == jsonschema.FalseSchema:
		s.AdditionalProperties = nil
	case s.AdditionalProperties != nil && s.AdditionalProperties != jsonschema.TrueSchema:
		AllowExtraProps(s.AdditionalProperties)
	}
	if s.Properties != nil {
		for pair := s.Properties.Oldest(); pair != nil; pair = pair.Next() {
			AllowExtraProps(pair.Value)
		}
	}
	AllowExtraProps(s.Items)
}

// Marshal renders s as two-space-indented JSON with a trailing newline.
func Marshal(s *jsonschema.Schema) ([]byte, error) {
	blob, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "marshal JSON schema")
	}
	return append(blob, '\n'), nil
}

// goSources returns the non-test Go files under dir in cb.
func goSources(
	ctx context.Context,
	cb interfaces.Codebase,
	dir string,
) ([]types.File, error) {
	files, err := cb.Files(ctx, types.FileSelector{
		Roots: []string{dir}, Exts: []string{".go"}, IncludeGenerated: true,
	})
	if err != nil {
		return nil, err
	}
	out := files[:0]
	for _, f := range files {
		if !strings.HasSuffix(f.Path, "_test.go") {
			out = append(out, f)
		}
	}
	return out, nil
}

// parseGo parses the Go file at p in cb with its comments.
func parseGo(
	ctx context.Context,
	cb interfaces.Codebase,
	fset *token.FileSet,
	p string,
) (*ast.File, error) {
	text, err := cb.Text(ctx, p)
	if err != nil {
		return nil, err
	}
	file, err := parser.ParseFile(fset, p, text.Bytes, parser.ParseComments)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "parse "+p)
	}
	return file, nil
}

// Comments returns the doc comments of the struct types and fields declared under dir
// in cb, keyed as Options.Comments expects; a file's package path is importPath
// joined with its directory relative to dir. Test files are skipped.
func Comments(
	ctx context.Context,
	cb interfaces.Codebase,
	dir, importPath string,
) (map[string]string, error) {
	files, err := goSources(ctx, cb, dir)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	fset := token.NewFileSet()
	for _, f := range files {
		file, err := parseGo(ctx, cb, fset, f.Path)
		if err != nil {
			return nil, err
		}
		pkgPath := importPath
		if rel := strings.TrimPrefix(path.Dir(f.Path), dir); rel != "" {
			// An import path is always slash-separated, so path (not filepath) is correct here.
			pkgPath = path.Join(importPath, strings.TrimPrefix(rel, "/")) // nosemgrep: use-filepath-join
		}
		collectStructDocs(file, pkgPath, out)
	}
	return out, nil
}

// collectStructDocs records the doc comment of each struct type in file and of its
// documented fields into out under pkgPath.
func collectStructDocs(file *ast.File, pkgPath string, out map[string]string) {
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			doc := ts.Doc
			if doc == nil {
				doc = gd.Doc
			}
			if doc != nil {
				out[pkgPath+"."+ts.Name.Name] = strings.TrimSpace(doc.Text())
			}
			for _, fld := range st.Fields.List {
				if fld.Doc == nil {
					continue
				}
				for _, nm := range fld.Names {
					out[pkgPath+"."+ts.Name.Name+"."+nm.Name] = strings.TrimSpace(
						fld.Doc.Text(),
					)
				}
			}
		}
	}
}
