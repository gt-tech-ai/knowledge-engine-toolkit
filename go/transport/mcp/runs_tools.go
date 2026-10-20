package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/runs"
)

// runIDSchema is the input schema of runs.status, runs.result and runs.cancel, hand
// reflected from runIDInput.
const runIDSchema = `{"type":"object","properties":{"run_id":{"type":"string",` +
	`"description":"The id a long-running tool returned"}},"required":["run_id"],` +
	`"additionalProperties":false}`

// emptySchema is the input schema of runs.list, which takes no arguments.
const emptySchema = `{"type":"object","additionalProperties":false}`

// runIDInput is the arguments of the runs.* tools that address one run.
type runIDInput struct {
	// RunID names the run.
	RunID string `json:"run_id"`
}

// RunStarted is what a long-running operation's tool returns: the id of the background
// run it started, to poll with runs.status and read with runs.result.
type RunStarted struct {
	// RunID names the run.
	RunID string `json:"run_id"`

	// State is always "running".
	State runs.State `json:"state"`
}

// RunStatus is what runs.status returns: a run's state and the steps it has finished.
type RunStatus struct {
	// RunID names the run.
	RunID string `json:"run_id"`

	// Op is the operation the run executes.
	Op string `json:"op"`

	// State is running, succeeded, failed, or cancelled.
	State runs.State `json:"state"`

	// Steps summarizes each finished step, in completion order (log paths appear only
	// in runs.result, once the run has ended).
	Steps []StepSummary `json:"steps"`
}

// RunEntry is one run as runs.list and runs.cancel report it.
type RunEntry struct {
	// RunID names the run.
	RunID string `json:"run_id"`

	// Op is the operation the run executes.
	Op string `json:"op"`

	// State is running, succeeded, failed, or cancelled.
	State runs.State `json:"state"`
}

// RunList is what runs.list returns: every run this server started, oldest first.
type RunList struct {
	// Runs lists the runs.
	Runs []RunEntry `json:"runs"`
}

// errorResult is the body of a runs.* tool call that failed before reaching a run.
type errorResult struct {
	// Error is the failure as "CODE: message".
	Error string `json:"error"`
}

// startHandler starts op as a background run for one tools/call and returns its run id
// at once; undecodable arguments or a refused start (a busy working tree) come back as
// the same ToolResult a bounded tool's failure does.
func (s *Server) startHandler(op interfaces.Operation) sdkmcp.ToolHandler {
	name := op.Descriptor().Name
	return func(
		ctx context.Context,
		req *sdkmcp.CallToolRequest,
	) (*sdkmcp.CallToolResult, error) {
		input := op.NewInput()
		if err := decodeArgs(req.Params.Arguments, input); err != nil {
			return s.result(
				nil,
				nil,
				apperr.Wrap(err, apperr.CodeInvalidInput, fmt.Sprintf(
					"operation %q: decode arguments", name,
				)),
			)
		}
		id, err := s.table.Start(ctx, name, input)
		if err != nil {
			return s.result(nil, nil, err)
		}
		return reply(RunStarted{RunID: id, State: runs.StateRunning}, false)
	}
}

// addRunsTools adds the runs.* tools that observe and stop the background runs.
func (s *Server) addRunsTools() {
	s.addRunTool("runs.status", "Report a background run's state and finished steps",
		func(id string) (*sdkmcp.CallToolResult, error) {
			snap, err := s.table.Status(id)
			if err != nil {
				return replyErr(err)
			}
			return reply(RunStatus{
				RunID: snap.ID, Op: snap.Op, State: snap.State,
				Steps: summarize(flatten(snap.Steps), snap.LogPaths),
			}, false)
		})
	s.addRunTool("runs.result",
		"Return a finished background run's result: step summaries, failed-step tails "+
			"and log paths",
		func(id string) (*sdkmcp.CallToolResult, error) {
			snap, err := s.table.Result(id)
			if err != nil {
				return replyErr(err)
			}
			res := buildResult(
				flatten(snap.Steps), s.settings.TailLines, snap.LogPaths, snap.Err,
			)
			return reply(res, snap.Err != nil)
		})
	s.addRunTool("runs.cancel", "Cancel a background run (a no-op once it has ended)",
		func(id string) (*sdkmcp.CallToolResult, error) {
			if err := s.table.Cancel(id); err != nil {
				return replyErr(err)
			}
			snap, err := s.table.Status(id)
			if err != nil {
				return replyErr(err)
			}
			return reply(entry(&snap), false)
		})
	s.srv.AddTool(&sdkmcp.Tool{
		Name:        "runs.list",
		Description: "List the background runs this server started, oldest first",
		InputSchema: json.RawMessage(emptySchema),
	}, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		list := RunList{Runs: []RunEntry{}}
		snaps := s.table.List()
		for i := range snaps {
			list.Runs = append(list.Runs, entry(&snaps[i]))
		}
		return reply(list, false)
	})
}

// addRunTool adds a runs.* tool taking {run_id}; a missing or undecodable run_id is an
// INVALID_INPUT error result, and fn answers for a present one.
func (s *Server) addRunTool(
	name, description string,
	fn func(id string) (*sdkmcp.CallToolResult, error),
) {
	s.srv.AddTool(&sdkmcp.Tool{
		Name:        name,
		Description: description,
		InputSchema: json.RawMessage(runIDSchema),
	}, func(_ context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		var in runIDInput
		if err := decodeArgs(req.Params.Arguments, &in); err != nil {
			return replyErr(
				apperr.Wrap(err, apperr.CodeInvalidInput, name+": decode arguments"),
			)
		}
		if in.RunID == "" {
			return replyErr(
				apperr.New(apperr.CodeInvalidInput, name+": run_id is required"),
			)
		}
		return fn(in.RunID)
	})
}

// entry is snap as runs.list reports it.
func entry(snap *runs.Snapshot) RunEntry {
	return RunEntry{RunID: snap.ID, Op: snap.Op, State: snap.State}
}

// reply returns v as JSON text and as structured content, with IsError set to failed.
func reply(v any, failed bool) (*sdkmcp.CallToolResult, error) {
	text, err := json.Marshal(v)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "mcp: encode tool result")
	}
	return &sdkmcp.CallToolResult{
		Content:           []sdkmcp.Content{&sdkmcp.TextContent{Text: string(text)}},
		StructuredContent: v,
		IsError:           failed,
	}, nil
}

// replyErr reports err as an error result {"error": "CODE: message"}.
func replyErr(err error) (*sdkmcp.CallToolResult, error) {
	return reply(errorResult{Error: codedMessage(err)}, true)
}
