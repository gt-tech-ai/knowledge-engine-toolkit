package unit_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/mcp"
)

// numberedLines returns n lines "line 1" … "line n", newline-terminated.
func numberedLines(n int) []byte {
	var builder strings.Builder

	for i := 1; i <= n; i++ {
		fmt.Fprintf(&builder, "line %d\n", i)
	}

	return []byte(builder.String())
}

// emitSteps returns a stub Run that reports each step to the observer and then
// returns err.
func emitSteps(
	err error,
	steps ...types.StepResult,
) func(context.Context, any, interfaces.ExecutionObserver) error {
	return func(_ context.Context, _ any, obs interfaces.ExecutionObserver) error {
		for _, s := range steps {
			obs.OnStepComplete(&s)
		}

		return err
	}
}

// callStub starts a server over reg with opts and calls the tool named "stub" with no
// arguments, returning the raw result and its decoded ToolResult.
func callStub(
	t *testing.T,
	reg mcp.Registry,
	opts *mcp.Options,
) (*sdkmcp.CallToolResult, mcp.ToolResult) {
	t.Helper()

	srv, err := mcp.New(reg, opts)
	require.NoError(t, err)

	res, err := connectMCP(t, srv).CallTool(context.Background(),
		fixtures.BuildRef(func(row *sdkmcp.CallToolParams) {
			row.Name = textStub
		}))
	require.NoError(t, err)

	var out mcp.ToolResult

	require.NoError(t, json.Unmarshal([]byte(toolText(t, res)), &out))

	return res, out
}

// TestTransportMcpToolResult_IsStepsPlusFailedTails tests that a tool result lists every
// step and carries output only for the failed ones.
//
// Why this test is important:
//   - An agent's context is small; a lint run's full output would drown it, so a
//     passing step must cost one line and a failing step only its tail.
//
// What it tests:
//   - a passing step with 100 lines of output and a failing step with 5 give two step
//     summaries (PASS, FAIL), a failed tail for the failing step only, the run's coded
//     error message, and IsError
//   - a failed group step keeps its own status, tail, and log beside its count-only
//     children, which follow it named "<group>/<child>"
func TestTransportMcpToolResult_IsStepsPlusFailedTails(t *testing.T) {
	t.Parallel()

	reg := stubOpRegistry(t, textStub, textTypeObject, emitSteps(
		apperr.New(apperr.CodeInvalidInput, "lint found issues"),
		fixtures.Build(func(row *types.StepResult) {
			row.Name = textGofmt
			row.Status = types.StatusPass
			row.Detail = numberedLines(100)
		}),
		fixtures.Build(func(row *types.StepResult) {
			row.Name = textGolangciLint
			row.Status = types.StatusFail
			row.Detail = numberedLines(5)
		}),
		fixtures.Build(func(row *types.StepResult) {
			row.Name = "tests"
			row.Status = types.StatusFail
			row.Detail = numberedLines(2)
			row.Children = []types.StepResult{
				fixtures.Build(func(row *types.StepResult) {
					row.Name = "pkg/a"
					row.Status = types.StatusPass
				}),
				fixtures.Build(func(row *types.StepResult) {
					row.Name = "pkg/b"
					row.Status = types.StatusPass
				}),
			}
		}),
	))
	res, out := callStub(t, reg, new(mcpOpts(t)))

	assert.True(t, res.IsError)

	got := make([]string, 0, len(out.Steps))
	for _, s := range out.Steps {
		got = append(got, s.Step+" "+s.Status)
	}

	assert.Equal(t, []string{
		"gofmt PASS",
		"golangci-lint FAIL",
		"tests FAIL",
		"tests/pkg/a PASS",
		"tests/pkg/b PASS",
	}, got)
	assert.NotEmpty(t, out.Steps[2].Log)
	assert.Empty(t, out.Steps[3].Log)
	assert.Equal(t, map[string]string{
		textGolangciLint: string(numberedLines(5)),
		"tests":          string(numberedLines(2)),
	}, out.FailedTails)
	assert.Contains(t, out.Error, "lint found issues")
}

// outputStepLines is the line count of the output step's answer, more than a failed
// step's tail keeps.
const outputStepLines = 60

// TestTransportMcpToolResult_ReturnsOutputSteps tests that a step marked as the
// operation's output is returned whole, even when it passed.
//
// Why this test is important:
//   - An operation that answers a question (a model's reply) is useless over MCP if the
//     caller gets only "PASS" and a log path; but returning every passing step's output
//     would flood the caller's context.
//
// What it tests:
//   - An OutputStep beside a passing step with 100 lines of output gives an output map
//     holding only the OutputStep's full text (not tailed), keyed by its step name, and
//     no failed tails or error.
func TestTransportMcpToolResult_ReturnsOutputSteps(t *testing.T) {
	t.Parallel()

	answer := string(numberedLines(outputStepLines))
	reg := stubOpRegistry(t, "stub", `{"type":"object"}`, emitSteps(
		nil,
		fixtures.Build(func(row *types.StepResult) {
			row.Name = "gofmt"
			row.Status = types.StatusPass
			row.Detail = numberedLines(100)
		}),
		types.OutputStep("answer", answer),
	))
	res, out := callStub(t, reg, new(mcpOpts(t)))

	assert.False(t, res.IsError)
	assert.Equal(t, map[string]string{"answer": answer}, out.Output)
	assert.Empty(t, out.FailedTails)
	assert.Empty(t, out.Error)
}

// TestTransportMcpFailedStepTail_RespectsConfigN tests that a failed step's tail is the
// configured number of trailing lines.
//
// Why this test is important:
//   - tail_lines is the knob that trades agent context for diagnosis; the result must
//     honour it rather than a hard-coded count.
//
// What it tests:
//   - with tail_lines 3, a failing step with 10 lines yields lines 8–10
//   - a failing step whose runner streamed its output (no captured lines) contributes
//     its step error instead, so the agent still sees why it failed
func TestTransportMcpFailedStepTail_RespectsConfigN(t *testing.T) {
	t.Parallel()

	reg := stubOpRegistry(t, textStub, textTypeObject, emitSteps(
		apperr.New(apperr.CodeInternal, textFailed),
		fixtures.Build(func(row *types.StepResult) {
			row.Name = textVet
			row.Status = types.StatusFail
			row.Detail = numberedLines(10)
		}),
		fixtures.Build(func(row *types.StepResult) {
			row.Name = "tsc"
			row.Status = types.StatusFail
			row.Error = "tsc: exit status 2"
		}),
	))
	opts := mcpOpts(t)

	opts.Settings.TailLines = initialTailLines

	_, out := callStub(t, reg, &opts)

	assert.Equal(t, "line 8\nline 9\nline 10\n", out.FailedTails[textVet])
	assert.Equal(t, "tsc: exit status 2", out.FailedTails["tsc"])
}

// TestTransportMcpStepLog_WrittenAndPathReturned tests that a run's step output is
// written under its run directory and each file's path is returned.
//
// Why this test is important:
//   - The result carries only a tail; the full output must be on disk where the agent
//     can read it, and nowhere another user can.
//
// What it tests:
//   - WriteSteps writes one owner-only file per step with output, whose content equals
//     the step's output, under <root>/<run id>/, with "/" in a step name sanitized; a
//     step without output gets no file and an empty path
//   - the paths are aligned with the steps, and steps whose names sanitize alike
//     (including one already named like a suffixed name) each keep their own file
func TestTransportMcpStepLog_WrittenAndPathReturned(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	rd := mcp.NewRunDir(root, time.Now)
	identifier := rd.NewRunID()
	logs, err := rd.WriteSteps(identifier, []types.StepResult{
		fixtures.Build(func(row *types.StepResult) {
			row.Name = "cmd/tool/x"
			row.Detail = []byte("ok\n")
		}),
		fixtures.Build(func(row *types.StepResult) {
			row.Name = "silent"
		}),
		fixtures.Build(func(row *types.StepResult) {
			row.Name = "cmd_tool_x-2"
			row.Detail = []byte("literal\n")
		}),
		fixtures.Build(func(row *types.StepResult) {
			row.Name = "cmd:tool:x"
			row.Detail = []byte("second\n")
		}),
		fixtures.Build(func(row *types.StepResult) {
			row.Name = "cmd tool x"
			row.Detail = []byte("third\n")
		}),
	})
	require.NoError(t, err)

	require.Len(t, logs, wantLogsLen)
	assert.Empty(t, logs[1])

	path := logs[0]
	assert.Equal(t, filepath.Join(root, identifier), filepath.Dir(path))
	assert.NotContains(t, filepath.Base(path), textSlash)
	assert.Equal(
		t,
		[]string{"cmd_tool_x-2.log", "cmd_tool_x-3.log", "cmd_tool_x-4.log"},
		[]string{filepath.Base(logs[2]), filepath.Base(logs[3]), filepath.Base(logs[4])},
	)

	for index, want := range map[int]string{
		0:                   "ok\n",
		iterations2Returned: "literal\n",
		iterations3Returned: "second\n",
		iterations4Returned: "third\n",
	} {
		got, rerr := os.ReadFile(logs[index])
		require.NoError(t, rerr)
		assert.Equal(t, want, string(got), "step %d", index)
	}

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(fileModeInput), info.Mode().Perm())
}

// TestTransportMcpOldRuns_PrunedAtStartup tests that serving keeps only the newest
// run_retention run directories, and that building the server touches none.
//
// Why this test is important:
//   - Every tool call writes a run directory; without a bound the history grows until
//     it fills the disk. Construction does no I/O, so a server built and never served
//     (a wiring test, a help listing) leaves the history alone.
//
// What it tests:
//   - 25 run directories with increasing run ids and run_retention 20: after New all 25
//     remain; after a session that ends at once, the 20 newest remain and the 5 oldest
//     are gone
func TestTransportMcpOldRuns_PrunedAtStartup(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	clock := time.Date(
		dateInput,
		dateInputStartup,
		dateInputAtStartup,
		dateInputPrunedAtStartup,
		0,
		0,
		0,
		time.UTC,
	)
	runDir := mcp.NewRunDir(root, func() time.Time { return clock })
	ids := make([]string, 0, makeInputStartup)

	for range iterationsStartup {
		id := runDir.NewRunID()
		require.NoError(t, os.MkdirAll(filepath.Join(root, id), mkdirAllInputStartup))

		ids = append(ids, id)
		clock = clock.Add(time.Second)
	}

	opts := mcpOpts(t)

	opts.RunDir = runDir

	srv, err := mcp.New(stubOpRegistry(t, textStub, textTypeObject, nil), &opts)
	require.NoError(t, err)
	assert.Equal(t, ids, runDirs(t, root))

	out, err := os.CreateTemp(t.TempDir(), "stdout")
	require.NoError(t, err)
	require.NoError(
		t,
		srv.ServeStdio(t.Context(), io.NopCloser(strings.NewReader("")), out),
	)
	assert.Equal(t, ids[5:], runDirs(t, root))
}

// runDirs lists the entries of root by name.
func runDirs(t *testing.T, root string) []string {
	t.Helper()

	entries, err := os.ReadDir(root)
	require.NoError(t, err)

	left := make([]string, 0, len(entries))
	for _, e := range entries {
		left = append(left, e.Name())
	}

	return left
}

// TestTransportMcpBoundedTool_ReturnsShapedResult tests that a bounded tool call returns
// the shaped result as both text and structured content, with each step's log path on
// disk.
//
// Why this test is important:
//   - Clients read either the text or the structured content; both must carry the same
//     result, and every log path in it must exist.
//
// What it tests:
//   - one passing and one failing step: structured content decodes to the same
//     ToolResult as the text, each step's Log names a file holding its output, and a
//     run that succeeds is not an error result
func TestTransportMcpBoundedTool_ReturnsShapedResult(t *testing.T) {
	t.Parallel()

	reg := stubOpRegistry(t, textStub, textTypeObject, emitSteps(
		nil,
		fixtures.Build(func(row *types.StepResult) {
			row.Name = textBuild
			row.Status = types.StatusPass
			row.Detail = []byte("built\n")
		}),
		fixtures.Build(func(row *types.StepResult) {
			row.Name = textWarn
			row.Status = types.StatusWarn
			row.Detail = []byte("careful\n")
		}),
	))
	res, out := callStub(t, reg, new(mcpOpts(t)))

	assert.False(t, res.IsError)
	assert.Empty(t, out.Error)

	raw, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)

	var structured mcp.ToolResult

	require.NoError(t, json.Unmarshal(raw, &structured))
	assert.Equal(t, out, structured)

	require.Len(t, out.Steps, wantStepsLen)

	for i, want := range []string{"built\n", "careful\n"} {
		got, err := os.ReadFile(out.Steps[i].Log)
		require.NoError(t, err, out.Steps[i].Step)
		assert.Equal(t, want, string(got))
	}
}

// TestTransportMcpNew_RejectsInvalidMCPSettings tests that the server refuses settings
// that would drop every tail or prune every run.
//
// Why this test is important:
//   - A manifest built in code skips load validation; the server must still refuse a
//     zero retention before its startup prune deletes the whole history.
//
// What it tests:
//   - tail_lines 0, run_retention 0, an absolute or escaping run_dir, and a zero
//     RunDir each fail New with CodeInvalidInput
func TestTransportMcpNew_RejectsInvalidMCPSettings(t *testing.T) {
	t.Parallel()

	reg := stubOpRegistry(t, textStub, textTypeObject, nil)
	for _, mutate := range []func(*mcp.Options){
		func(o *mcp.Options) { o.Settings.TailLines = 0 },
		func(o *mcp.Options) { o.Settings.RunRetention = 0 },
		func(o *mcp.Options) { o.Settings.RunDir = "/var/tmp/runs" },
		func(o *mcp.Options) { o.Settings.RunDir = "../runs" },
		func(o *mcp.Options) { o.RunDir = mcp.RunDir{} },
	} {
		opts := mcpOpts(t)
		mutate(&opts)

		_, err := mcp.New(reg, &opts)
		require.Error(t, err)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	}
}

// TestTransportMcpServe_PruneFailure tests a run directory that cannot be pruned.
//
// Why this test is important:
//   - An operator reading "transport failed" looks at the client and the pipes; when
//     the run directory is what failed, the message must say so.
//
// What it tests:
//   - ServeStdio over a run-directory root that is a regular file fails before the
//     session starts, with an error naming the run directory and not the transport.
func TestTransportMcpServe_PruneFailure(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "runs")
	require.NoError(t, os.WriteFile(root, []byte(textX), writeFileInput))

	opts := mcpOpts(t)

	opts.RunDir = mcp.NewRunDir(root, time.Now)

	srv, err := mcp.New(stubOpRegistry(t, textStub, textTypeObject, nil), &opts)
	require.NoError(t, err)

	out, err := os.CreateTemp(t.TempDir(), "stdout")
	require.NoError(t, err)

	err = srv.ServeStdio(t.Context(), io.NopCloser(strings.NewReader("")), out)
	require.ErrorContains(t, err, "run dir")
	assert.NotContains(t, err.Error(), "transport")
}

const (
	// dateInputAtStartup is the day passed to Date in
	// TestTransportMcpOldRuns_PrunedAtStartup.
	dateInputAtStartup = 4

	// dateInputPrunedAtStartup is the hour passed to Date in
	// TestTransportMcpOldRuns_PrunedAtStartup.
	dateInputPrunedAtStartup = 12

	// dateInputStartup is the month passed to Date in
	// TestTransportMcpOldRuns_PrunedAtStartup.
	dateInputStartup = 10

	// initialTailLines is the value assigned to opts.Settings.TailLines in
	// TestTransportMcpFailedStepTail_RespectsConfigN.
	initialTailLines = 3

	// iterations2Returned is a map key in TestTransportMcpStepLog_WrittenAndPathReturned.
	iterations2Returned = 2

	// iterations3Returned is a map key in TestTransportMcpStepLog_WrittenAndPathReturned.
	iterations3Returned = 3

	// iterations4Returned is a map key in TestTransportMcpStepLog_WrittenAndPathReturned.
	iterations4Returned = 4

	// iterationsStartup is the iteration count in
	// TestTransportMcpOldRuns_PrunedAtStartup.
	iterationsStartup = 25

	// makeInputStartup is argument 3 of make in TestTransportMcpOldRuns_PrunedAtStartup.
	makeInputStartup = 25

	// mkdirAllInputStartup is the perm passed to MkdirAll in
	// TestTransportMcpOldRuns_PrunedAtStartup.
	mkdirAllInputStartup = 0o750

	// wantLogsLen is the expected logs len.
	wantLogsLen = 5
)
