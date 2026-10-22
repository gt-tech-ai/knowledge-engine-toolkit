package unit_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/ops"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/mcp"
)

// slowOp is a long-running stub operation: each run reports one PASS step "build" with
// two lines of output, signals started, then blocks until release yields or its
// context ends; ended counts the runs whose context ended them.
type slowOp struct {
	started chan struct{}
	release chan struct{}
	ended   atomic.Int64
}

// newSlowOp returns a slowOp whose channels hold a few signals without blocking.
func newSlowOp() *slowOp {
	return &slowOp{started: make(chan struct{}, 4), release: make(chan struct{}, 4)}
}

// run is the operation body the stub registry calls.
func (o *slowOp) run(ctx context.Context, _ any, obs interfaces.ExecutionObserver) error {
	obs.OnStepComplete(types.StepResult{
		Name: "build", Status: types.StatusPass, Detail: []byte("one\ntwo\n"),
	})
	o.started <- struct{}{}
	select {
	case <-o.release:
		return nil
	case <-ctx.Done():
		o.ended.Add(1)
		return apperr.Wrap(ctx.Err(), apperr.CodeCanceled, "Context.Err")
	}
}

// registry builds a registry admitting only this op, as long-running tool "slow".
func (o *slowOp) registry(t *testing.T) *ops.Registry {
	t.Helper()
	return stubOpRegistryLC(t, "slow", `{"type":"object"}`,
		types.OpLifecycleLongRunning, o.run)
}

// callJSON calls tool name with args and decodes its text content into out, returning
// the result's IsError flag and raw text.
func callJSON(
	t *testing.T,
	cs *sdkmcp.ClientSession,
	name string,
	args map[string]any,
	out any,
) (isError bool, text string) {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &sdkmcp.CallToolParams{
		Name: name, Arguments: args,
	})
	require.NoError(t, err)
	text = toolText(t, res)
	if out != nil {
		require.NoError(t, json.Unmarshal([]byte(text), out), text)
	}
	return res.IsError, text
}

// awaitRunState polls runs.status for id until it reports want, failing after 2s.
func awaitRunState(
	t *testing.T,
	cs *sdkmcp.ClientSession,
	id, want string,
) mcp.RunStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var st mcp.RunStatus
		isErr, text := callJSON(t, cs, "runs.status", map[string]any{"run_id": id}, &st)
		require.False(t, isErr, text)
		if string(st.State) == want {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s: state %s, want %s", id, st.State, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestTransportMcpLongRunningTool_ReturnsRunID tests that calling a long-running
// operation's tool starts a background run and returns at once.
//
// Why this test is important:
//   - A preflight or integration run outlasts an MCP client's tool-call timeout; the
//     call must hand back a run id to poll rather than hold the request open.
//
// What it tests:
//   - calling "slow" returns a non-error {run_id, state:"running"} while the operation
//     is still blocked; once released, runs.status reports it succeeded
func TestTransportMcpLongRunningTool_ReturnsRunID(t *testing.T) {
	t.Parallel()

	op := newSlowOp()
	srv, err := mcp.New(op.registry(t), mcpOpts(t))
	require.NoError(t, err)
	cs := connectMCP(t, srv)

	var started mcp.RunStarted
	isErr, text := callJSON(t, cs, "slow", nil, &started)
	require.False(t, isErr, text)
	assert.NotEmpty(t, started.RunID)
	assert.Equal(t, "running", string(started.State))
	<-op.started

	op.release <- struct{}{}
	awaitRunState(t, cs, started.RunID, "succeeded")
}

// TestTransportMcpRunsTools_StatusResultCancelList tests the four runs.* tools over a
// run's life.
//
// Why this test is important:
//   - They are the agent's only view of a background run: what it has done so far, its
//     final result with log paths, stopping it, and finding it again.
//
// What it tests:
//   - runs.status lists the "build" step while running; runs.result fails with
//     CONFLICT until it ends, then returns the ToolResult with a log path under the
//     run directory holding the step's output
//   - runs.cancel stops a second run, which runs.status then reports cancelled
//   - runs.list holds both runs with their ops and states, oldest first
//   - an unknown run id is a NOT_FOUND error result and a missing one INVALID_INPUT
func TestTransportMcpRunsTools_StatusResultCancelList(t *testing.T) {
	t.Parallel()

	op := newSlowOp()
	opts := mcpOpts(t)
	root := t.TempDir()
	opts.RunDir = mcp.NewRunDir(root, time.Now)
	srv, err := mcp.New(op.registry(t), opts)
	require.NoError(t, err)
	cs := connectMCP(t, srv)

	var first mcp.RunStarted
	callJSON(t, cs, "slow", nil, &first)
	<-op.started
	idArg := map[string]any{"run_id": first.RunID}

	st := awaitRunState(t, cs, first.RunID, "running")
	require.Len(t, st.Steps, 1)
	assert.Equal(t, "build", st.Steps[0].Step)
	assert.Equal(t, "slow", st.Op)
	isErr, text := callJSON(t, cs, "runs.result", idArg, nil)
	assert.True(t, isErr)
	assert.Contains(t, text, "CONFLICT")

	op.release <- struct{}{}
	awaitRunState(t, cs, first.RunID, "succeeded")
	var res mcp.ToolResult
	isErr, text = callJSON(t, cs, "runs.result", idArg, &res)
	require.False(t, isErr, text)
	require.Len(t, res.Steps, 1)
	log := res.Steps[0].Log
	assert.Equal(t, root, filepath.Dir(filepath.Dir(log)))
	body, err := os.ReadFile(log)
	require.NoError(t, err)
	assert.Equal(t, "one\ntwo\n", string(body))

	var second mcp.RunStarted
	callJSON(t, cs, "slow", nil, &second)
	<-op.started
	isErr, text = callJSON(
		t,
		cs,
		"runs.cancel",
		map[string]any{"run_id": second.RunID},
		nil,
	)
	require.False(t, isErr, text)
	awaitRunState(t, cs, second.RunID, "cancelled")

	var list mcp.RunList
	isErr, text = callJSON(t, cs, "runs.list", nil, &list)
	require.False(t, isErr, text)
	assert.Equal(t, []mcp.RunEntry{
		{RunID: first.RunID, Op: "slow", State: "succeeded"},
		{RunID: second.RunID, Op: "slow", State: "cancelled"},
	}, list.Runs)

	isErr, text = callJSON(t, cs, "runs.status", map[string]any{"run_id": "nope"}, nil)
	assert.True(t, isErr)
	assert.Contains(t, text, "NOT_FOUND")
	isErr, text = callJSON(t, cs, "runs.status", nil, nil)
	assert.True(t, isErr)
	assert.Contains(t, text, "INVALID_INPUT")
}

// stdioSession starts srv.ServeStdio(ctx) over in-process pipes and connects a client
// session to it, returning the session, the client's write end of the server's stdin,
// and a channel carrying ServeStdio's result.
func stdioSession(
	ctx context.Context,
	t *testing.T,
	srv *mcp.Server,
) (*sdkmcp.ClientSession, *io.PipeWriter, <-chan error) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- srv.ServeStdio(ctx, inR, outW) }()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test", Version: "test"}, nil)
	cs, err := client.Connect(ctx,
		&sdkmcp.IOTransport{Reader: outR, Writer: inW}, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = cs.Close()
		_ = outR.Close()
	})
	return cs, inW, done
}

// TestTransportMcpEOF_CancelsActiveRunsBeforeExit tests that a client disconnect stops
// every background run before the server returns.
//
// Why this test is important:
//   - A run outliving its server would keep mutating the working tree with nobody able
//     to see or stop it.
//
// What it tests:
//   - with "slow" running, closing the client's stdin makes ServeStdio return nil, and
//     by then the run's context has ended it
func TestTransportMcpEOF_CancelsActiveRunsBeforeExit(t *testing.T) {
	t.Parallel()

	op := newSlowOp()
	srv, err := mcp.New(op.registry(t), mcpOpts(t))
	require.NoError(t, err)
	cs, inW, done := stdioSession(t.Context(), t, srv)
	callJSON(t, cs, "slow", nil, &mcp.RunStarted{})
	<-op.started

	require.NoError(t, inW.Close())
	require.NoError(t, awaitServe(t, done))
	assert.Equal(t, int64(1), op.ended.Load())
}

// TestTransportMcpCancel_CancelsActiveRunsBeforeExit tests that a signal stops every
// background run before the server returns.
//
// Why this test is important:
//   - Ctrl-C / SIGTERM cancel the serve context; the process must not exit with an
//     operation still running beneath it.
//
// What it tests:
//   - with "slow" running, cancelling ServeStdio's context makes it return nil, and by
//     then the run's context has ended it
func TestTransportMcpCancel_CancelsActiveRunsBeforeExit(t *testing.T) {
	t.Parallel()

	op := newSlowOp()
	srv, err := mcp.New(op.registry(t), mcpOpts(t))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cs, inW, done := stdioSession(ctx, t, srv)
	t.Cleanup(func() { _ = inW.Close() })
	callJSON(t, cs, "slow", nil, &mcp.RunStarted{})
	<-op.started

	cancel()
	require.NoError(t, awaitServe(t, done))
	assert.Equal(t, int64(1), op.ended.Load())
}

// TestTransportMcpLongRunningTool_BusyReplyIsToolResult tests that a long-running tool
// refused for a busy working tree answers in the same shape as a bounded tool's failure.
//
// Why this test is important:
//   - An agent reads every operation tool's failure the same way; a long-running tool
//     that answered with a different body would hide its "steps" key and break that
//     reader exactly when it needs to learn which run holds the tree.
//
// What it tests:
//   - while a "slow" run holds the slot, a second call is an error result whose JSON
//     carries a "steps" array and an error naming "UNAVAILABLE: busy"
func TestTransportMcpLongRunningTool_BusyReplyIsToolResult(t *testing.T) {
	t.Parallel()

	op := newSlowOp()
	srv, err := mcp.New(op.registry(t), mcpOpts(t))
	require.NoError(t, err)
	cs := connectMCP(t, srv)

	var started mcp.RunStarted
	isErr, text := callJSON(t, cs, "slow", nil, &started)
	require.False(t, isErr, text)
	<-op.started

	var busy map[string]any
	isErr, text = callJSON(t, cs, "slow", nil, &busy)
	assert.True(t, isErr)
	assert.IsType(t, []any{}, busy["steps"], text)
	assert.Contains(t, busy["error"], "UNAVAILABLE: busy")

	op.release <- struct{}{}
	awaitRunState(t, cs, started.RunID, "succeeded")
}

// TestTransportMcpRunsTools_OnlyWithLongRunningOperation tests that the runs.* tools are
// listed only when some listed operation runs in the background.
//
// Why this test is important:
//   - A policy that admits no long-running operation (a single-check policy, a
//     bounded-only policy) would otherwise advertise four tools that can never find a
//     run.
//
// What it tests:
//   - a registry holding one bounded operation lists exactly that tool
func TestTransportMcpRunsTools_OnlyWithLongRunningOperation(t *testing.T) {
	t.Parallel()

	reg := stubOpRegistryLC(t, "stub", `{"type":"object"}`, types.OpLifecycleBounded,
		func(context.Context, any, interfaces.ExecutionObserver) error { return nil })
	srv, err := mcp.New(reg, mcpOpts(t))
	require.NoError(t, err)
	cs := connectMCP(t, srv)

	res, err := cs.ListTools(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, res.Tools, 1)
	assert.Equal(t, "stub", res.Tools[0].Name)
}
