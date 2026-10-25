package codebase

import (
	"context"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/yamlq"
)

// yamlView is the YAML view of a Codebase.
type yamlView struct {
	// parent supplies files and contents.
	parent *view
}

// Compile-time assertion that yamlView satisfies YAMLView.
var _ interfaces.YAMLView = (*yamlView)(nil)

// Docs lists every document of every YAML file sel selects; with no Exts in sel it
// keeps `.yaml` and `.yml` files.
func (y *yamlView) Docs(
	ctx context.Context,
	sel types.FileSelector,
) ([]types.YAMLDoc, error) {
	if len(sel.Exts) == 0 {
		sel.Exts = []string{extYAML, extYML}
	}
	files, err := y.parent.Files(ctx, sel)
	if err != nil {
		return nil, err
	}
	var out []types.YAMLDoc
	for _, f := range files {
		nodes, err := y.nodes(ctx, f.Path)
		if err != nil {
			return nil, err
		}
		for i := range nodes {
			out = append(out, types.YAMLDoc{Path: f.Path, Index: i})
		}
	}
	return out, nil
}

// Query selects path's nodes in doc.
func (y *yamlView) Query(
	ctx context.Context,
	doc types.YAMLDoc,
	path string,
) ([]types.YAMLMatch, error) {
	node, err := y.node(ctx, doc)
	if err != nil {
		return nil, err
	}
	matches, err := yamlq.Path(node, path)
	if err != nil {
		return nil, err
	}
	out := make([]types.YAMLMatch, 0, len(matches))
	for _, m := range matches {
		out = append(out, types.YAMLMatch{Value: m.Value, Kind: m.Kind, Line: m.Line})
	}
	return out, nil
}

// Decode decodes doc into out.
func (y *yamlView) Decode(ctx context.Context, doc types.YAMLDoc, out any) error {
	node, err := y.node(ctx, doc)
	if err != nil {
		return err
	}
	if err := node.Decode(out); err != nil {
		return apperr.Wrap(err, apperr.CodeInvalidInput,
			fmt.Sprintf("decode %s document %d", doc.Path, doc.Index))
	}
	return nil
}

// node returns one document's node.
func (y *yamlView) node(ctx context.Context, doc types.YAMLDoc) (*yaml.Node, error) {
	nodes, err := y.nodes(ctx, doc.Path)
	if err != nil {
		return nil, err
	}
	if doc.Index < 0 || doc.Index >= len(nodes) {
		return nil, apperr.New(apperr.CodeNotFound,
			fmt.Sprintf("%s has no document %d", doc.Path, doc.Index))
	}
	return nodes[doc.Index], nil
}

// nodes decodes a file's documents once per snapshot, dropping empty ones (a lone
// `---`).
func (y *yamlView) nodes(ctx context.Context, p string) ([]*yaml.Node, error) {
	return y.parent.snap.Memo(
		ctx,
		"yaml|"+p,
		func(ctx context.Context) ([]*yaml.Node, error) {
			text, err := y.parent.Text(ctx, p)
			if err != nil {
				return nil, err
			}
			docs, err := yamlq.DecodeAll(text.Bytes)
			if err != nil {
				return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "parse "+p)
			}
			out := docs[:0]
			for _, d := range docs {
				if len(d.Content) > 0 && !emptyDoc(d.Content[0]) {
					out = append(out, d)
				}
			}
			return out, nil
		},
	)
}

// emptyDoc reports whether a document's root is the implicit null of an empty document.
func emptyDoc(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null" &&
		strings.TrimSpace(n.Value) == ""
}
