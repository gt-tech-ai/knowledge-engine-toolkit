package helm

import (
	"context"
	"encoding/json"
	"strings"

	"go.yaml.in/yaml/v3"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/codebase"
)

// sourcePrefix starts the comment helm writes above each rendered document.
const sourcePrefix = "# Source: "

// renderer renders charts through a HelmClient, once per spec per snapshot.
type renderer struct {
	// helm renders and fetches dependencies.
	helm interfaces.HelmClient

	// snap memoizes renders and dependency builds.
	snap *codebase.Snapshot
}

// NewRenderer returns the Renderer over c: each chart's dependencies are built once
// per snapshot before its first render, each spec renders once, and the output is
// parsed into objects that name their source template.
func NewRenderer(c interfaces.HelmClient, s *codebase.Snapshot) interfaces.Renderer {
	return &renderer{helm: c, snap: s}
}

// Render renders spec into objects.
func (r *renderer) Render(
	ctx context.Context,
	spec types.RenderSpec,
) ([]types.Object, error) {
	key, err := json.Marshal(spec)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "render spec")
	}
	return r.snap.Memo(ctx, "helm|template|"+string(key),
		func(ctx context.Context) ([]types.Object, error) {
			if err := BuildDeps(ctx, r.helm, r.snap, spec.ChartDir); err != nil {
				return nil, err
			}
			out, err := r.helm.Template(ctx, spec)
			if err != nil {
				return nil, err
			}
			return Parse(out)
		})
}

// Parse splits a rendered manifest at its `---` lines into objects, skipping empty
// documents; each object's Source is the template named in its `# Source:` comment.
func Parse(manifest []byte) ([]types.Object, error) {
	var out []types.Object
	for _, chunk := range documents(string(manifest)) {
		var doc map[string]any
		if err := yaml.Unmarshal([]byte(chunk), &doc); err != nil {
			return nil, apperr.Wrap(
				err,
				apperr.CodeInvalidInput,
				"decode rendered document",
			)
		}
		if len(doc) == 0 {
			continue
		}
		meta, _ := doc["metadata"].(map[string]any)
		o := types.Object{Doc: doc, Index: len(out), Source: sourceOf(chunk)}
		o.Kind, _ = doc["kind"].(string)
		o.Name, _ = meta["name"].(string)
		o.Namespace, _ = meta["namespace"].(string)
		out = append(out, o)
	}
	return out, nil
}

// documents splits a manifest at its `---` separator lines.
func documents(manifest string) []string {
	var out []string
	var cur strings.Builder
	for line := range strings.SplitSeq(manifest, "\n") {
		if strings.TrimRight(line, " \r") == "---" {
			out = append(out, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteString(line + "\n")
	}
	return append(out, cur.String())
}

// sourceOf is the value of a document's `# Source:` comment, or "".
func sourceOf(doc string) string {
	for line := range strings.SplitSeq(doc, "\n") {
		if s, ok := strings.CutPrefix(line, sourcePrefix); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}
