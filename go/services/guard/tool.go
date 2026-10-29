package guard

import (
	"bytes"
	"context"

	"go.yaml.in/yaml/v3"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// toolCheck runs r's tool with its args in root, the objects r's chart renders on its
// stdin as YAML documents (nothing when r renders no chart). A render failure is a
// finding naming the chart, and the tool does not run; a non-zero exit is one finding
// carrying the tool's output.
func toolCheck(
	ctx context.Context,
	cb interfaces.Codebase,
	r *types.GuardRow,
	runner interfaces.CommandRunner,
	root string,
) ([]types.Finding, error) {
	if runner == nil {
		return nil, apperr.New(apperr.CodeInternal, "no runner for tool "+r.Tool)
	}
	var input []byte
	if r.Render.ChartDir != "" {
		objs, err := cb.Rendered(ctx, r.Render)
		if err != nil { //nolint:nilerr // a chart that fails to render is the row's finding
			return []types.Finding{
				{Message: "render " + r.Render.ChartDir + ": " + err.Error()},
			}, nil
		}
		if input, err = documents(objs); err != nil {
			return nil, err
		}
	}
	res := runner.RunBufferedWithInput(ctx, root, nil, input, r.Tool, r.Args...)
	if res.Err == nil {
		return nil, nil
	}
	subject := r.Tool + " rejected"
	if r.Render.ChartDir != "" {
		subject += " " + r.Render.ChartDir
	}
	return []types.Finding{{
		Message: subject + ": " + string(res.Stdout) + string(res.Stderr),
	}}, nil
}

// documents encodes objs as a YAML stream, one document per object.
func documents(objs []types.Object) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	for i := range objs {
		if err := enc.Encode(objs[i].Doc); err != nil {
			return nil, apperr.Wrap(err, apperr.CodeInternal, "encode a rendered object")
		}
	}
	if err := enc.Close(); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "encode the rendered objects")
	}
	return buf.Bytes(), nil
}
