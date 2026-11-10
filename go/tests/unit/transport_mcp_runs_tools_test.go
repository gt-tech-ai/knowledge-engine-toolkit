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

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/ops"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
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
	return &slowOp{
		started: make(chan struct{}, makeInputOp),
		release: make(chan struct{}, makeInputOp),
		ended:   atomic.Int64{},
	}
}

// run is the operation body the stub registry calls.
func (o *slowOp) run(ctx context.Context, _ any, obs interfaces.ExecutionObserver) error {
	obs.OnStepComplete(fixtures.BuildRef(func(row *types.StepResult) {
		row.Name = textBuild
		row.Status = types.StatusPass
		row.Detail = []byte("one\ntwo\n")
	}))

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

	return stubOpRegistryLC(t, textSlow, textTypeObject,
		types.OpLifecycleLongRunning, o.run)
}

// callJSON calls tool name with args and decodes its text content into out, returning
// the result's IsError flag and raw text.
func callJSON(
	t *testing.T,
	clientSession *sdkmcp.ClientSession,
	name string,
	args map[string]any,
	out any,
) (isError bool, text string) {
	t.Helper()

	res, err := clientSession.CallTool(
		t.Context(),
		fixtures.BuildRef(func(row *sdkmcp.CallToolParams) {
			row.Name = name
			row.Arguments = args
		}),
	)
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
	clientSession *sdkmcp.ClientSession,
	identifier, want string,
) mcp.RunStatus {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)

	for {
		var runStatus mcp.RunStatus

		isErr, text := callJSON(
			t,
			clientSession,
			textRunsStatus,
			map[string]any{textRunID: identifier},
			&runStatus,
		)
		require.False(t, isErr, text)

		if string(runStatus.State) == want {
			return runStatus
		}

		if time.Now().After(deadline) {
			t.Fatalf("run %s: state %s, want %s", identifier, runStatus.State, want)
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

	operation := newSlowOp()
	srv, err := mcp.New(operation.registry(t), new(mcpOpts(t)))
	require.NoError(t, err)

	clientSession := connectMCP(t, srv)

	var started mcp.RunStarted

	isErr, text := callJSON(t, clientSession, textSlow, nil, &started)
	require.False(t, isErr, text)
	assert.NotEmpty(t, started.RunID)
	assert.Equal(t, "running", string(started.State))
	<-operation.started

	operation.release <- struct{}{}

	awaitRunState(t, clientSession, started.RunID, textSucceeded)
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

	operation := newSlowOp()
	opts := mcpOpts(t)
	root := t.TempDir()

	opts.RunDir = mcp.NewRunDir(root, time.Now)

	srv, err := mcp.New(operation.registry(t), &opts)
	require.NoError(t, err)

	clientSession := connectMCP(t, srv)

	var first mcp.RunStarted

	t.Helper()

	testTransportMcpRunsToolsStatusResultCancelListStatusAndErrors(
		t,
		operation,
		root,
		clientSession,
		first,
	)
}

// testTransportMcpRunsToolsStatusResultCancelListStatusAndErrors continues
// testTransportMcpRunsToolsStatusResultCancelList: status and errors.
func testTransportMcpRunsToolsStatusResultCancelListStatusAndErrors(
	t *testing.T,
	operation *slowOp,
	root string,
	clientSession *sdkmcp.ClientSession,
	first mcp.RunStarted,
) {
	t.Helper()

	var err error

	callJSON(t, clientSession, textSlow, nil, &first)
	<-operation.started

	idArg := map[string]any{textRunID: first.RunID}

	st := awaitRunState(t, clientSession, first.RunID, "running")
	require.Len(t, st.Steps, 1)
	assert.Equal(t, textBuild, st.Steps[0].Step)
	assert.Equal(t, textSlow, st.Op)

	isErr, text := callJSON(t, clientSession, "runs.result", idArg, nil)
	assert.True(t, isErr)
	assert.Contains(t, text, "CONFLICT")

	operation.release <- struct{}{}

	awaitRunState(t, clientSession, first.RunID, textSucceeded)

	var res mcp.ToolResult

	isErr, text = callJSON(t, clientSession, "runs.result", idArg, &res)
	require.False(t, isErr, text)
	require.Len(t, res.Steps, 1)

	log := res.Steps[0].Log
	assert.Equal(t, root, filepath.Dir(filepath.Dir(log)))

	body, err := os.ReadFile(filepath.Clean(log))
	require.NoError(t, err)
	assert.Equal(t, "one\ntwo\n", string(body))

	testTransportMcpRunsToolsStatusResultCancelListResultsAndList(
		t,
		operation,
		clientSession,
		&first,
	)
}

// testTransportMcpRunsToolsStatusResultCancelListResultsAndList continues
// testTransportMcpRunsToolsStatusResultCancelListStatusAndErrors: results and list.
func testTransportMcpRunsToolsStatusResultCancelListResultsAndList(
	t *testing.T,
	operation *slowOp,
	clientSession *sdkmcp.ClientSession,
	first *mcp.RunStarted,
) {
	t.Helper()

	var text string

	var isErr bool

	var second mcp.RunStarted

	callJSON(t, clientSession, textSlow, nil, &second)
	<-operation.started

	isErr, text = callJSON(
		t,
		clientSession,
		"runs.cancel",
		map[string]any{textRunID: second.RunID},
		nil,
	)
	require.False(t, isErr, text)
	awaitRunState(t, clientSession, second.RunID, "cancelled")

	var list mcp.RunList

	isErr, text = callJSON(t, clientSession, "runs.list", nil, &list)
	require.False(t, isErr, text)
	assert.Equal(t, []mcp.RunEntry{
		{RunID: first.RunID, Op: textSlow, State: textSucceeded},
		{RunID: second.RunID, Op: textSlow, State: "cancelled"},
	}, list.Runs)

	isErr, text = callJSON(
		t,
		clientSession,
		textRunsStatus,
		map[string]any{textRunID: textNope},
		nil,
	)
	assert.True(t, isErr)
	assert.Contains(t, text, "NOT_FOUND")

	isErr, text = callJSON(t, clientSession, textRunsStatus, nil, nil)
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

	client := sdkmcp.NewClient(
		fixtures.BuildRef(func(row *sdkmcp.Implementation) {
			row.Name = textTest
			row.Version = textTest
		}),
		nil,
	)
	clientSession, err := client.Connect(ctx,
		&sdkmcp.IOTransport{Reader: outR, Writer: inW, MaxLineLength: 0}, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = outR.Close()
	})

	return clientSession, inW, done
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

	operation := newSlowOp()
	srv, err := mcp.New(operation.registry(t), new(mcpOpts(t)))
	require.NoError(t, err)

	cs, inW, done := stdioSession(t.Context(), t, srv)
	callJSON(t, cs, textSlow, nil, new(mcp.RunStarted))
	<-operation.started

	require.NoError(t, inW.Close())
	require.NoError(t, awaitServe(t, done))
	assert.Equal(t, int64(1), operation.ended.Load())
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

	operation := newSlowOp()
	srv, err := mcp.New(operation.registry(t), new(mcpOpts(t)))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	cs, inW, done := stdioSession(ctx, t, srv)
	t.Cleanup(func() { _ = inW.Close() })
	callJSON(t, cs, textSlow, nil, new(mcp.RunStarted))
	<-operation.started

	cancel()
	require.NoError(t, awaitServe(t, done))
	assert.Equal(t, int64(1), operation.ended.Load())
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

	operation := newSlowOp()
	srv, err := mcp.New(operation.registry(t), new(mcpOpts(t)))
	require.NoError(t, err)

	clientSession := connectMCP(t, srv)

	var started mcp.RunStarted

	isErr, text := callJSON(t, clientSession, textSlow, nil, &started)
	require.False(t, isErr, text)
	<-operation.started

	var busy map[string]any

	isErr, text = callJSON(t, clientSession, textSlow, nil, &busy)
	assert.True(t, isErr)
	assert.IsType(t, []any{}, busy["steps"], text)
	assert.Contains(t, busy[textError], "UNAVAILABLE: busy")

	operation.release <- struct{}{}

	awaitRunState(t, clientSession, started.RunID, textSucceeded)
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

	reg := stubOpRegistryLC(t, textStub, textTypeObject, types.OpLifecycleBounded,
		func(context.Context, any, interfaces.ExecutionObserver) error { return nil })
	srv, err := mcp.New(reg, new(mcpOpts(t)))
	require.NoError(t, err)

	cs := connectMCP(t, srv)

	res, err := cs.ListTools(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, res.Tools, 1)
	assert.Equal(t, textStub, res.Tools[0].Name)
}

const (
	// makeInputOp is argument 2 of make in newSlowOp.
	makeInputOp = 4
)

const (
	// textRunID is the literal "run_id".
	textRunID = "run_id"

	// textSucceeded is the literal "succeeded".
	textSucceeded = "succeeded"
)
