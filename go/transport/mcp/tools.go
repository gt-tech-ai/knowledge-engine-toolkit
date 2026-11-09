package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/ops"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/runs"
)

// toolFor describes op as an MCP tool: its name, its summary as the description, and
// its own input schema verbatim. A schema that is not a JSON object with "type":
// "object" fails with CodeInternal naming the operation.
func toolFor(op interfaces.Operation) (*sdkmcp.Tool, error) {
	operationDescriptor := op.Descriptor()
	schema := op.InputSchema()

	var top struct {
		Type string `json:"type"`
	}

	if json.Unmarshal(schema, &top) != nil || top.Type != "object" {
		return nil, apperr.New(apperr.CodeInternal, fmt.Sprintf(
			"operation %q: input schema is not a JSON object schema",
			operationDescriptor.Name,
		))
	}

	var tool sdkmcp.Tool

	tool.Name = operationDescriptor.Name
	tool.Description = operationDescriptor.Summary
	tool.InputSchema = json.RawMessage(schema)

	return &tool, nil
}

// handler runs a bounded op for one tools/call: it decodes the arguments into a fresh
// input (ops.Decode: rejecting unknown fields and secret flags), runs it through the
// registry under a collecting observer, writes each step's output under a new run
// directory, and returns the shaped ToolResult as both JSON text and structured content.
// A failure sets IsError, so the agent reads it instead of receiving a protocol error; a
// log-write failure is joined onto the run's own error, which keeps its code.
func (s *Server) handler(operation interfaces.Operation) sdkmcp.ToolHandler {
	name := operation.Descriptor().Name

	return func(
		ctx context.Context,
		req *sdkmcp.CallToolRequest,
	) (*sdkmcp.CallToolResult, error) {
		// ops.Decode refuses an argument the input does not declare, a secret flag
		// included: a credential never travels as a tool argument.
		input, err := ops.Decode(operation, req.Params.Arguments)
		if err != nil {
			return s.result(nil, nil, err)
		}

		obs := &runs.Collector{}

		err = s.reg.Run(ctx, name, input, obs)

		steps := flatten(obs.Steps())
		logs, werr := s.runs.WriteSteps(s.runs.NewRunID(), steps)

		return s.result(steps, logs, apperr.Join(err, werr))
	}
}

// result shapes a finished run's flattened steps as the tool result: the ToolResult as
// JSON text and as structured content, with IsError set when err is non-nil.
func (s *Server) result(
	steps []types.StepResult,
	logs []string,
	err error,
) (*sdkmcp.CallToolResult, error) {
	return reply(buildResult(steps, s.settings.TailLines, logs, err), err != nil)
}

// decodeArgs decodes raw tool arguments into input, rejecting unknown fields; absent
// or null arguments leave input zero-valued.
func decodeArgs(raw json.RawMessage, input any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	return apperr.Wrap(dec.Decode(input), apperr.CodeInvalidInput, "Decoder.Decode")
}
