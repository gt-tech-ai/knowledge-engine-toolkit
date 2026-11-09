package unit_test

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/ops"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/mcp"
)

// mcpOpts names the server the transport tests start and gives it the default MCP
// settings over a run directory in a fresh temp dir.
func mcpOpts(t *testing.T) mcp.Options {
	t.Helper()

	return mcp.Options{
		Name:    textTools,
		Version: textTest,
		Settings: types.MCPSettings{
			TailLines: tailLines, RunRetention: runRetention, RunDir: ".runs",
		},
		RunDir: mcp.NewRunDir(t.TempDir(), time.Now),
	}
}

// mcpRegistry builds a registry over mixedDescs, each a succeeding Func operation over
// stubInput, under an agent's policy: read-only and mutating classes, bounded and
// long-running lifecycles.
func mcpRegistry(t *testing.T) *ops.Registry {
	t.Helper()

	descs := mixedDescs()
	builders := make(map[string]ops.Builder[opsEnv], len(descs))

	for _, d := range descs {
		builders[d.Name] = ops.Func(func(
			context.Context, *stubInput, opsEnv, interfaces.ExecutionObserver,
		) error {
			return nil
		})
	}

	reg, err := ops.NewFromConfig(ops.Config[opsEnv]{
		Policy: fixtures.Build(func(row *types.ExposurePolicy) {
			row.MaxSafety = types.SafetyMutating
			row.Lifecycles = []types.OpLifecycle{
				types.OpLifecycleBounded, types.OpLifecycleLongRunning,
			}
		}),
		Descriptors: descs,
		Builders:    builders,
		Env:         stdx.Zero[opsEnv](),
		Log:         nil,
	})
	require.NoError(t, err)

	return reg
}

// connectMCP starts srv on one end of an in-memory transport pair and returns a client
// session on the other; both stop when the test ends.
func connectMCP(t *testing.T, srv *mcp.Server) *sdkmcp.ClientSession {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	serverT, clientT := sdkmcp.NewInMemoryTransports()
	done := make(chan error, 1)

	go func() { done <- srv.Serve(ctx, serverT) }()

	client := sdkmcp.NewClient(
		fixtures.BuildRef(func(row *sdkmcp.Implementation) {
			row.Name = textTest
			row.Version = textTest
		}),
		nil,
	)
	clientSession, err := client.Connect(ctx, clientT, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = clientSession.Close()

		cancel()
		<-done
	})

	return clientSession
}

// stubOpRegistry builds a registry under an all-admitting policy over one bounded mock
// operation named name, publishing schema and running run.
func stubOpRegistry(
	t *testing.T,
	name, schema string,
	run func(ctx context.Context, input any, obs interfaces.ExecutionObserver) error,
) *ops.Registry {
	t.Helper()

	return stubOpRegistryLC(t, name, schema, types.OpLifecycleBounded, run)
}

// stubOpRegistryLC is stubOpRegistry with the operation's lifecycle chosen by lc.
func stubOpRegistryLC(
	t *testing.T,
	name, schema string,
	opLifecycle types.OpLifecycle,
	run func(ctx context.Context, input any, obs interfaces.ExecutionObserver) error,
) *ops.Registry {
	t.Helper()

	ctrl := gomock.NewController(t)
	desc := opDesc(name, types.SafetyReadOnly, opLifecycle)
	reg, err := ops.NewFromConfig(ops.Config[opsEnv]{
		Policy: fixtures.Build(func(row *types.ExposurePolicy) {
			row.MaxSafety = types.SafetyCredentialed
			row.Lifecycles = allLifecycles()
		}),
		Descriptors: []types.OperationDescriptor{desc},
		Builders: map[string]ops.Builder[opsEnv]{
			name: func(d types.OperationDescriptor, _ opsEnv) interfaces.Operation {
				operation := mocks.NewMockOperation(ctrl)
				operation.EXPECT().Descriptor().Return(d).AnyTimes()
				operation.EXPECT().InputSchema().Return([]byte(schema)).AnyTimes()
				operation.EXPECT().
					NewInput().
					DoAndReturn(func() any {
						return new(stubInput)
					}).
					AnyTimes()

				if run != nil {
					operation.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).
						DoAndReturn(run).AnyTimes()
				}

				return operation
			},
		},
		Env: stdx.Zero[opsEnv](),
		Log: nil,
	})
	require.NoError(t, err)

	return reg
}

// stubInput is the input the stub operation decodes its tool arguments into.
type stubInput struct {
	// Go is the one flag the stub operation accepts.
	Go bool `json:"go"`
}

// TestTransportMcpServe_ListsOnlyMCPPolicyOperations tests that the MCP server
// advertises exactly the operations its exposure policy admits.
//
// Why this test is important:
//   - An agent sees only the tools listed; a destructive or credentialed operation
//     reaching the list would let it run one.
//
// What it tests:
//   - tools/list names equal the registry's operations plus the four runs.*
//     tools, no operation has the destructive or credentialed class or the
//     interactive lifecycle, and each
//     operation tool's description is its summary
func TestTransportMcpServe_ListsOnlyMCPPolicyOperations(t *testing.T) {
	t.Parallel()

	reg := mcpRegistry(t)
	srv, err := mcp.New(reg, new(mcpOpts(t)))
	require.NoError(t, err)

	cs := connectMCP(t, srv)

	res, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)

	want := map[string]types.OperationDescriptor{}
	wantNames := make([]string, 0, len(reg.List())+4)

	for _, op := range reg.List() {
		operationDescriptor := op.Descriptor()

		want[operationDescriptor.Name] = operationDescriptor
		wantNames = append(wantNames, operationDescriptor.Name)
		assert.NotContains(
			t,
			[]types.SafetyClass{
				types.SafetyDestructive,
				types.SafetyCredentialed,
			},
			operationDescriptor.SafetyClass,
			operationDescriptor.Name,
		)
		assert.NotEqual(
			t,
			types.OpLifecycleInteractive,
			operationDescriptor.Lifecycle,
			operationDescriptor.Name,
		)
	}

	require.NotEmpty(t, wantNames)

	wantNames = append(
		wantNames,
		"runs.cancel",
		"runs.list",
		"runs.result",
		textRunsStatus,
	)
	slices.Sort(wantNames)

	got := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
		if d, ok := want[tool.Name]; ok {
			assert.Equal(t, d.Summary, tool.Description, tool.Name)
		}
	}

	slices.Sort(got)
	assert.Equal(t, wantNames, got)
}

// TestTransportMcpToolInputSchema_MatchesOperation tests that each tool advertises its
// operation's own input schema.
//
// Why this test is important:
//   - The schema is how an agent learns which arguments an operation accepts; a
//     transport that rewrote it would advertise flags the operation rejects.
//
// What it tests:
//   - every listed operation tool's inputSchema JSON-equals op.InputSchema() (the
//     runs.* tools have no operation)
func TestTransportMcpToolInputSchema_MatchesOperation(t *testing.T) {
	t.Parallel()

	reg := mcpRegistry(t)
	srv, err := mcp.New(reg, new(mcpOpts(t)))
	require.NoError(t, err)

	cs := connectMCP(t, srv)

	res, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.NotEmpty(t, res.Tools)

	for _, tool := range res.Tools {
		if strings.HasPrefix(tool.Name, "runs.") {
			continue
		}

		op, ok := reg.Lookup(tool.Name)
		require.True(t, ok, tool.Name)

		got, err := json.Marshal(tool.InputSchema)
		require.NoError(t, err)
		assert.JSONEq(t, string(op.InputSchema()), string(got), tool.Name)
	}
}

// TestTransportMcpNew_RejectsNonObjectSchema tests that the server refuses an operation
// whose input schema is not a JSON object schema.
//
// Why this test is important:
//   - The SDK panics on such a tool; the server must fail construction with a coded
//     error instead of crashing the process mid-registration.
//
// What it tests:
//   - an array schema and invalid JSON each fail New with CodeInternal naming the
//     operation
func TestTransportMcpNew_RejectsNonObjectSchema(t *testing.T) {
	t.Parallel()

	for _, schema := range []string{`{"type":"array"}`, textNotStructured} {
		_, err := mcp.New(stubOpRegistry(t, textBad, schema, nil), new(mcpOpts(t)))
		require.Error(t, err, schema)
		assert.Equal(t, apperr.CodeInternal, apperr.Code(err), schema)
		assert.Contains(t, err.Error(), `"bad"`, schema)
	}
}

// TestTransportMcpCallTool_RunsOperation tests that tools/call decodes the arguments
// into the operation's input and reports its outcome as the tool result.
//
// Why this test is important:
//   - A tool call is the agent's only way to run an operation; a failure must reach it as
//     an error result it can read, not a protocol error that hides the cause.
//
// What it tests:
//   - {"go":true} reaches Run as &stubInput{Go:true} and a nil error gives a non-error
//     ToolResult with no error message; a coded failure gives IsError with the message;
//     an unknown argument gives IsError without running the operation
func TestTransportMcpCallTool_RunsOperation(t *testing.T) {
	t.Parallel()

	var runs []stubInput

	fail := false
	reg := stubOpRegistry(t, textStub, textTypeObject,
		func(_ context.Context, input any, _ interfaces.ExecutionObserver) error {
			in, ok := input.(*stubInput)
			if !assert.True(t, ok) {
				return apperr.New(apperr.CodeInternal, "stub got a non-stubInput input")
			}

			runs = append(runs, *in)

			if fail {
				return apperr.New(apperr.CodeInvalidInput, "stub refused")
			}

			return nil
		})
	srv, err := mcp.New(reg, new(mcpOpts(t)))
	require.NoError(t, err)

	clientSession := connectMCP(t, srv)
	ctx := context.Background()

	res, err := clientSession.CallTool(
		ctx,
		fixtures.BuildRef(func(row *sdkmcp.CallToolParams) {
			row.Name = textStub
			row.Arguments = map[string]any{textGoLower: true}
		}),
	)

	testTransportMcpCallToolRunsOperationChecksResult(ctx,
		t,
		&runs,
		&fail,
		err,
		clientSession,
		res,
	)
}

// testTransportMcpCallToolRunsOperationChecksResult continues
// TestTransportMcpCallTool_RunsOperation.
func testTransportMcpCallToolRunsOperationChecksResult(ctx context.Context,
	t *testing.T,
	runs *[]stubInput,
	fail *bool,
	err error,
	clientSession *sdkmcp.ClientSession,
	res *sdkmcp.CallToolResult,
) {
	t.Helper()

	require.NoError(t, err)
	assert.False(t, res.IsError)

	var ok mcp.ToolResult

	require.NoError(t, json.Unmarshal([]byte(toolText(t, res)), &ok))
	assert.Empty(t, ok.Error)
	assert.Empty(t, ok.FailedTails)
	assert.Equal(t, []stubInput{{Go: true}}, (*runs))

	(*fail) = true
	res, err = clientSession.CallTool(
		ctx,
		fixtures.BuildRef(func(row *sdkmcp.CallToolParams) {
			row.Name = textStub
		}),
	)
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, toolText(t, res), "stub refused")

	res, err = clientSession.CallTool(
		ctx,
		fixtures.BuildRef(func(row *sdkmcp.CallToolParams) {
			row.Name = textStub
			row.Arguments = map[string]any{textBogus: 1}
		}),
	)
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, toolText(t, res), textBogus)
	assert.Len(t, (*runs), wantRunsLen)
}

// toolText returns the text of a tool result's single text content.
func toolText(t *testing.T, res *sdkmcp.CallToolResult) string {
	t.Helper()
	require.Len(t, res.Content, 1)

	text, ok := res.Content[0].(*sdkmcp.TextContent)
	require.True(t, ok)

	return text.Text
}

// serveStdioPipes starts srv.ServeStdio over in-process pipes and returns the client's
// write end of stdin and a channel carrying ServeStdio's result.
func serveStdioPipes(
	ctx context.Context,
	t *testing.T,
	srv *mcp.Server,
) (clientIn *io.PipeWriter, serveResult <-chan error) {
	t.Helper()

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()

	go func() { _, _ = io.Copy(io.Discard, outR) }()

	t.Cleanup(func() { _ = outR.Close() })

	done := make(chan error, 1)

	go func() { done <- srv.ServeStdio(ctx, inR, outW) }()

	return inW, done
}

// awaitServe waits up to two seconds for ServeStdio to return.
func awaitServe(t *testing.T, done <-chan error) error {
	t.Helper()

	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("ServeStdio did not return within 2s")

		return nil
	}
}

// TestTransportMcpServer_ShutsDownOnEOF tests that the stdio server exits cleanly when
// its client closes stdin.
//
// Why this test is important:
//   - An agent client ends the session by closing the pipe; the server must exit 0, not
//     report the disconnect as a failure or hang.
//
// What it tests:
//   - closing the client's end of stdin makes ServeStdio return nil within 2s
func TestTransportMcpServer_ShutsDownOnEOF(t *testing.T) {
	t.Parallel()

	srv, err := mcp.New(mcpRegistry(t), new(mcpOpts(t)))
	require.NoError(t, err)

	inW, done := serveStdioPipes(context.Background(), t, srv)

	require.NoError(t, inW.Close())
	assert.NoError(t, awaitServe(t, done))
}

// TestTransportMcpServer_ShutsDownOnCancel tests that the stdio server exits cleanly
// when its context is cancelled.
//
// Why this test is important:
//   - SIGINT / SIGTERM cancel the command's context; the server must stop and exit 0
//     rather than block on a stdin read.
//
// What it tests:
//   - cancelling the context makes ServeStdio return nil within 2s
func TestTransportMcpServer_ShutsDownOnCancel(t *testing.T) {
	t.Parallel()

	srv, err := mcp.New(mcpRegistry(t), new(mcpOpts(t)))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	inW, done := serveStdioPipes(ctx, t, srv)
	t.Cleanup(func() { _ = inW.Close() })

	cancel()
	assert.NoError(t, awaitServe(t, done))
}

// TestTransportMcpServer_FailsOnTransportError tests that a stdin failure other than a
// clean close surfaces as a coded error.
//
// Why this test is important:
//   - Treating every stream error as a shutdown would hide a broken transport behind
//     exit 0.
//
// What it tests:
//   - stdin failing with a non-EOF error makes ServeStdio return CodeInternal
func TestTransportMcpServer_FailsOnTransportError(t *testing.T) {
	t.Parallel()

	srv, err := mcp.New(mcpRegistry(t), new(mcpOpts(t)))
	require.NoError(t, err)

	inW, done := serveStdioPipes(context.Background(), t, srv)

	require.NoError(t, inW.CloseWithError(apperr.Sentinel("disk on fire")))

	err = awaitServe(t, done)
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
}

// TestTransportMcpCallTool_RefusesASecretArgument tests that an MCP caller can neither
// see nor send a map operation's secret flag.
//
// Why this test is important:
//   - A tool argument is recorded in the agent's transcript and the run log, so a
//     credential must never travel as one; the operation's CLI form still takes it.
//
// What it tests:
//   - the tool's input schema omits the secret flag; a call sending it is an error
//     result naming the key and the operation never runs; a call with only the declared
//     flags runs.
func TestTransportMcpCallTool_RefusesASecretArgument(t *testing.T) {
	t.Parallel()

	ran := 0
	desc := opDesc("kb", types.SafetyReadOnly, types.OpLifecycleBounded)
	reg, err := ops.NewFromConfig(ops.Config[opsEnv]{
		Policy: fixtures.Build(func(row *types.ExposurePolicy) {
			row.MaxSafety = types.SafetyCredentialed
			row.Lifecycles = allLifecycles()
		}),
		Descriptors: []types.OperationDescriptor{desc},
		Builders: map[string]ops.Builder[opsEnv]{"kb": ops.MapInput([]types.FlagSpec{
			fixtures.Build(func(row *types.FlagSpec) {
				row.Name = textClientID
				row.Type = types.FlagString
			}),
			fixtures.Build(func(row *types.FlagSpec) {
				row.Name = textClientSecretKebab
				row.Type = types.FlagString
				row.Secret = true
			}),
		}, stdx.Zero[types.ArgSpec](), func(
			context.Context, map[string]any, opsEnv, interfaces.ExecutionObserver,
		) error {
			ran++

			return nil
		})},
		Env: stdx.Zero[opsEnv](),
		Log: nil,
	})
	require.NoError(t, err)

	srv, err := mcp.New(reg, new(mcpOpts(t)))
	require.NoError(t, err)

	clientSession := connectMCP(t, srv)

	testTransportMcpCallToolRefusesASecretArgumentChecksRefusal(t, &ran, clientSession)
}

// testTransportMcpCallToolRefusesASecretArgumentChecksRefusal continues
// TestTransportMcpCallTool_RefusesASecretArgument.
func testTransportMcpCallToolRefusesASecretArgumentChecksRefusal(
	t *testing.T,
	ran *int,
	clientSession *sdkmcp.ClientSession,
) {
	t.Helper()

	ctx := context.Background()

	tools, err := clientSession.ListTools(ctx, nil)
	require.NoError(t, err)

	for _, tool := range tools.Tools {
		if tool.Name == "kb" {
			blob, merr := json.Marshal(tool.InputSchema)
			require.NoError(t, merr)
			assert.NotContains(t, string(blob), textClientSecret)
		}
	}

	res, err := clientSession.CallTool(
		ctx,
		fixtures.BuildRef(func(row *sdkmcp.CallToolParams) {
			row.Name = "kb"
			row.Arguments = map[string]any{textClientSecret: "s3cr3t"}
		}),
	)
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, toolText(t, res), textClientSecret)
	assert.Zero(t, (*ran), "a refused call must not run")

	res, err = clientSession.CallTool(
		ctx,
		fixtures.BuildRef(func(row *sdkmcp.CallToolParams) {
			row.Name = "kb"
			row.Arguments = map[string]any{"client_id": textA}
		}),
	)
	require.NoError(t, err)
	assert.False(t, res.IsError, toolText(t, res))
	assert.Equal(t, 1, (*ran))
}

const (
	// runRetention is the run retention the tests use.
	runRetention = 20

	// tailLines is the tail lines the tests use.
	tailLines = 40
)
