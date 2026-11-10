package guard

import (
	"bytes"
	"context"

	"go.yaml.in/yaml/v3"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// toolCheck runs r's tool with its args in root, the objects r's chart renders on its
// stdin as YAML documents (nothing when r renders no chart). A render failure is a
// finding naming the chart, and the tool does not run; a non-zero exit is one finding
// carrying the tool's output.
func toolCheck(
	ctx context.Context,
	codebase interfaces.Codebase,
	guardRow *types.GuardRow,
	runner interfaces.CommandRunner,
	root string,
) ([]types.Finding, error) {
	if runner == nil {
		return nil, apperr.New(apperr.CodeInternal, "no runner for tool "+guardRow.Tool)
	}

	input, rendered, err := renderToolInput(ctx, codebase, guardRow)
	if err != nil || rendered != nil {
		return rendered, err
	}

	res := runner.RunBufferedWithInput(
		ctx,
		root,
		nil,
		input,
		guardRow.Tool,
		guardRow.Args...)
	if res.Err == nil {
		return nil, nil
	}

	subject := guardRow.Tool + " rejected"
	if guardRow.Render.ChartDir != "" {
		subject += " " + guardRow.Render.ChartDir
	}

	return []types.Finding{{
		Message: subject + ": " + string(res.Stdout) + string(res.Stderr),
		Check:   "",
		File:    "",
		Rule:    "",
		Line:    0,
	}}, nil
}

// renderToolInput renders the row's chart to the tool's stdin. A chart that fails to
// render is the row's finding, returned instead of an error.
func renderToolInput(
	ctx context.Context, codebase interfaces.Codebase, guardRow *types.GuardRow,
) ([]byte, []types.Finding, error) {
	if guardRow.Render.ChartDir == "" {
		return nil, nil, nil
	}

	objs, err := codebase.Rendered(ctx, &guardRow.Render)
	if err == nil {
		input, derr := documents(objs)

		return input, nil, derr
	}

	return nil, []types.Finding{
		{
			Message: "render " + guardRow.Render.ChartDir + ": " + err.Error(),
			Check:   "",
			File:    "",
			Rule:    "",
			Line:    0,
		},
	}, nil
}

// documents encodes objs as a YAML stream, one document per object.
func documents(objs []types.Object) ([]byte, error) {
	var buf bytes.Buffer

	enc := yaml.NewEncoder(&buf)

	for i := range objs {
		err := enc.Encode(objs[i].Doc)
		if err != nil {
			return nil, apperr.Wrap(err, apperr.CodeInternal, "encode a rendered object")
		}
	}

	err := enc.Close()
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "encode the rendered objects")
	}

	return buf.Bytes(), nil
}
