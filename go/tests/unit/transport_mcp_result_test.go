package unit_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/mcp"
)

// numberedLines returns n lines "line 1" … "line n", newline-terminated.
func numberedLines(n int) []byte {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return []byte(b.String())
}

// emitSteps returns a stub Run that reports each step to the observer and then
// returns err.
func emitSteps(
	err error,
	steps ...types.StepResult,
) func(context.Context, any, interfaces.ExecutionObserver) error {
	return func(_ context.Context, _ any, obs interfaces.ExecutionObserver) error {
		for _, s := range steps {
			obs.OnStepComplete(s)
		}
		return err
	}
}

// callStub starts a server over reg with opts and calls the tool named "stub" with no
// arguments, returning the raw result and its decoded ToolResult.
func callStub(
	t *testing.T,
	reg mcp.Registry,
	opts mcp.Options,
) (*sdkmcp.CallToolResult, mcp.ToolResult) {
	t.Helper()
	srv, err := mcp.New(reg, opts)
	require.NoError(t, err)
	res, err := connectMCP(t, srv).CallTool(context.Background(),
		&sdkmcp.CallToolParams{Name: "stub"})
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

	reg := stubOpRegistry(t, "stub", `{"type":"object"}`, emitSteps(
		apperr.New(apperr.CodeInvalidInput, "lint found issues"),
		types.StepResult{
			Name:   "gofmt",
			Status: types.StatusPass,
			Detail: numberedLines(100),
		},
		types.StepResult{
			Name:   "golangci-lint",
			Status: types.StatusFail,
			Detail: numberedLines(5),
		},
		types.StepResult{
			Name:   "tests",
			Status: types.StatusFail,
			Detail: numberedLines(2),
			Children: []types.StepResult{
				{Name: "pkg/a", Status: types.StatusPass},
				{Name: "pkg/b", Status: types.StatusPass},
			},
		},
	))
	res, out := callStub(t, reg, mcpOpts(t))

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
		"golangci-lint": string(numberedLines(5)),
		"tests":         string(numberedLines(2)),
	}, out.FailedTails)
	assert.Contains(t, out.Error, "lint found issues")
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

	reg := stubOpRegistry(t, "stub", `{"type":"object"}`, emitSteps(
		apperr.New(apperr.CodeInternal, "failed"),
		types.StepResult{
			Name:   "vet",
			Status: types.StatusFail,
			Detail: numberedLines(10),
		},
		types.StepResult{
			Name:   "tsc",
			Status: types.StatusFail,
			Error:  "tsc: exit status 2",
		},
	))
	opts := mcpOpts(t)
	opts.Settings.TailLines = 3
	_, out := callStub(t, reg, opts)

	assert.Equal(t, "line 8\nline 9\nline 10\n", out.FailedTails["vet"])
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
	id := rd.NewRunID()
	logs, err := rd.WriteSteps(id, []types.StepResult{
		{Name: "apps/go/cli", Detail: []byte("ok\n")},
		{Name: "silent"},
		{Name: "apps_go_cli-2", Detail: []byte("literal\n")},
		{Name: "apps:go:cli", Detail: []byte("second\n")},
		{Name: "apps go cli", Detail: []byte("third\n")},
	})
	require.NoError(t, err)

	require.Len(t, logs, 5)
	assert.Empty(t, logs[1])
	path := logs[0]
	assert.Equal(t, filepath.Join(root, id), filepath.Dir(path))
	assert.NotContains(t, filepath.Base(path), "/")
	assert.Equal(
		t,
		[]string{"apps_go_cli-2.log", "apps_go_cli-3.log", "apps_go_cli-4.log"},
		[]string{filepath.Base(logs[2]), filepath.Base(logs[3]), filepath.Base(logs[4])},
	)
	for i, want := range map[int]string{
		0: "ok\n",
		2: "literal\n",
		3: "second\n",
		4: "third\n",
	} {
		got, rerr := os.ReadFile(logs[i])
		require.NoError(t, rerr)
		assert.Equal(t, want, string(got), "step %d", i)
	}
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// TestTransportMcpOldRuns_PrunedAtStartup tests that starting the MCP server keeps only
// the newest run_retention run directories.
//
// Why this test is important:
//   - Every tool call writes a run directory; without a bound the history grows until
//     it fills the disk.
//
// What it tests:
//   - 25 run directories with increasing run ids and run_retention 20: after New, the
//     20 newest remain and the 5 oldest are gone
func TestTransportMcpOldRuns_PrunedAtStartup(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	clock := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	rd := mcp.NewRunDir(root, func() time.Time { return clock })
	ids := make([]string, 0, 25)
	for range 25 {
		id := rd.NewRunID()
		require.NoError(t, os.MkdirAll(filepath.Join(root, id), 0o750))
		ids = append(ids, id)
		clock = clock.Add(time.Second)
	}

	opts := mcpOpts(t)
	opts.RunDir = rd
	_, err := mcp.New(stubOpRegistry(t, "stub", `{"type":"object"}`, nil), opts)
	require.NoError(t, err)

	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	left := make([]string, 0, len(entries))
	for _, e := range entries {
		left = append(left, e.Name())
	}
	assert.Equal(t, ids[5:], left)
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

	reg := stubOpRegistry(t, "stub", `{"type":"object"}`, emitSteps(
		nil,
		types.StepResult{
			Name:   "build",
			Status: types.StatusPass,
			Detail: []byte("built\n"),
		},
		types.StepResult{
			Name:   "warn",
			Status: types.StatusWarn,
			Detail: []byte("careful\n"),
		},
	))
	res, out := callStub(t, reg, mcpOpts(t))

	assert.False(t, res.IsError)
	assert.Empty(t, out.Error)
	raw, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	var structured mcp.ToolResult
	require.NoError(t, json.Unmarshal(raw, &structured))
	assert.Equal(t, out, structured)

	require.Len(t, out.Steps, 2)
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

	reg := stubOpRegistry(t, "stub", `{"type":"object"}`, nil)
	for _, mutate := range []func(*mcp.Options){
		func(o *mcp.Options) { o.Settings.TailLines = 0 },
		func(o *mcp.Options) { o.Settings.RunRetention = 0 },
		func(o *mcp.Options) { o.Settings.RunDir = "/var/tmp/runs" },
		func(o *mcp.Options) { o.Settings.RunDir = "../runs" },
		func(o *mcp.Options) { o.RunDir = mcp.RunDir{} },
	} {
		opts := mcpOpts(t)
		mutate(&opts)
		_, err := mcp.New(reg, opts)
		require.Error(t, err)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	}
}
