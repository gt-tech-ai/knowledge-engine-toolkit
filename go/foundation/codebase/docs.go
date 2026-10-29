package codebase

import (
	"context"
	"encoding/json"
	"path"

	"github.com/tmccombs/hcl2json/convert"
	"go.yaml.in/yaml/v3"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// The document formats Docs and Decode read, and the extensions a YAML selector keeps by
// default.
const (
	// formatYAML decodes YAML (and JSON, a YAML subset).
	formatYAML = "yaml"

	// formatHCL decodes HCL as Terraform's JSON syntax.
	formatHCL = "hcl"

	// formatAlloy decodes an Alloy (River) configuration.
	formatAlloy = "alloy"

	// formatTOML decodes TOML; its dates and times are timestamps.
	formatTOML = "toml"

	// extYAML and extYML are the YAML file extensions.
	extYAML, extYML = ".yaml", ".yml"
)

// docFormat is the document format of a file extension; "" is none.
func docFormat(ext string) string {
	switch ext {
	case extYAML, extYML, ".json":
		return formatYAML
	case ".tf", ".hcl", ".tfvars":
		return formatHCL
	case ".alloy", ".river":
		return formatAlloy
	case ".toml":
		return formatTOML
	default:
		return ""
	}
}

// Docs decodes every configuration file sel selects by its extension; with no Exts in
// sel it keeps `.yaml` and `.yml` files.
func (v *view) Docs(
	ctx context.Context,
	sel types.FileSelector,
) ([]types.Document, error) {
	if len(sel.Exts) == 0 {
		sel.Exts = []string{extYAML, extYML}
	}
	files, err := v.Files(ctx, sel)
	if err != nil {
		return nil, err
	}
	var out []types.Document
	for _, f := range files {
		format := docFormat(path.Ext(f.Path))
		var docs []any
		switch format {
		case formatYAML:
			docs, err = v.yamlDocs(ctx, f.Path)
		case formatHCL:
			docs, err = v.oneDoc(ctx, formatHCL, f.Path, decodeHCL)
		case formatAlloy:
			docs, err = v.oneDoc(ctx, formatAlloy, f.Path, decodeAlloy)
		case formatTOML:
			docs, err = v.oneDoc(ctx, formatTOML, f.Path, decodeTOML)
		default:
			continue
		}
		if err != nil {
			return nil, err
		}
		for i, d := range docs {
			out = append(
				out,
				types.Document{Value: d, Path: f.Path, Format: format, Index: i},
			)
		}
	}
	return out, nil
}

// yamlDocs decodes every document of a YAML file.
func (v *view) yamlDocs(ctx context.Context, p string) ([]any, error) {
	nodes, err := v.yamlView.nodes(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(nodes))
	for i := range nodes {
		var doc any
		if err := v.yamlView.Decode(
			ctx,
			types.YAMLDoc{Path: p, Index: i},
			&doc,
		); err != nil {
			return nil, err
		}
		out = append(out, doc)
	}
	return out, nil
}

// oneDoc decodes a single-document file once per snapshot.
func (v *view) oneDoc(
	ctx context.Context,
	format, p string,
	decode func(src []byte, p string) (any, error),
) ([]any, error) {
	doc, err := v.snap.Memo(
		ctx,
		"doc|"+format+"|"+p,
		func(ctx context.Context) (any, error) {
			text, err := v.Text(ctx, p)
			if err != nil {
				return nil, err
			}
			doc, err := decode(text.Bytes, p)
			if err != nil {
				return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "parse "+p)
			}
			return doc, nil
		},
	)
	if err != nil {
		return nil, err
	}
	return []any{doc}, nil
}

// Decode decodes src, named name for errors, in format ("yaml", "hcl" or "alloy") into
// the Document shape: the first YAML document, or the one HCL / Alloy document. It serves
// configuration embedded in another file (an Alloy config inside a Helm values string).
// An unknown format or a parse error is CodeInvalidInput.
func Decode(format, name string, src []byte) (any, error) {
	var doc any
	var err error
	switch format {
	case formatYAML:
		err = yaml.Unmarshal(src, &doc)
	case formatHCL:
		doc, err = decodeHCL(src, name)
	case formatAlloy:
		doc, err = decodeAlloy(src, name)
	case formatTOML:
		doc, err = decodeTOML(src, name)
	default:
		return nil, apperr.New(apperr.CodeInvalidInput, "decode: unknown format "+format)
	}
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "parse "+name+" as "+format)
	}
	return doc, nil
}

// decodeHCL decodes an HCL file as Terraform's JSON syntax, through hcl2json:
// literal-only expressions are evaluated, every other expression is its `${…}`-wrapped
// source text.
func decodeHCL(src []byte, p string) (any, error) {
	b, err := convert.Bytes(src, p, convert.Options{Simplify: true})
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "hcl "+p)
	}
	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "hcl json "+p)
	}
	return doc, nil
}
