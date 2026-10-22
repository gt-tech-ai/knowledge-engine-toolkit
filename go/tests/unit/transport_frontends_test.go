package unit_test

import (
	"context"
	"io"
	"sync"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/ops"
	opscobra "github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/cobra"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/cron"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/mcp"
)

// syncInput is the input of the operation every front-end runs: a flag, a bool flag
// and a positional argument.
type syncInput struct {
	// Env is the environment to sync.
	Env string `json:"env" flag:"env,e,environment to sync"`

	// Target is the first positional argument.
	Target string `json:"target" arg:"0"`

	// Fix applies the changes.
	Fix bool `json:"fix" flag:"fix,,apply the changes"`
}

// TestTransportFrontends_OneOperationThreeWays tests one registered operation run
// through every front-end.
//
// Why this test is important:
//   - The point of the operations registry is that a developer at a terminal, an agent
//     over MCP and a scheduled job run the same operation on the same input; a
//     front-end that decoded its arguments differently would run something else.
//
// What it tests:
//   - One operation registered once runs through the Cobra command (flags and a
//     positional argument), an MCP tool call (JSON arguments) and a cron run (raw
//     JSON), and each run receives the same decoded input.
func TestTransportFrontends_OneOperationThreeWays(t *testing.T) {
	t.Parallel()
	var (
		mu   sync.Mutex
		seen []syncInput
	)
	reg, err := ops.NewFromConfig(ops.Config[opsEnv]{
		Policy: types.ExposurePolicy{
			MaxSafety: types.SafetyMutating, Lifecycles: allLifecycles(),
		},
		Descriptors: []types.OperationDescriptor{
			opDesc("sync", types.SafetyMutating, types.OpLifecycleBounded),
		},
		Builders: map[string]ops.Builder[opsEnv]{
			"sync": ops.Func(func(
				_ context.Context, in *syncInput, _ opsEnv, _ interfaces.ExecutionObserver,
			) error {
				mu.Lock()
				defer mu.Unlock()
				seen = append(seen, *in)
				return nil
			}),
		},
	})
	require.NoError(t, err)

	cmd, err := opscobra.Command(reg, "sync", opscobra.Options{})
	require.NoError(t, err)
	cmd.SetArgs([]string{"-e", "staging", "--fix", "api"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	require.NoError(t, cmd.ExecuteContext(t.Context()))

	srv, err := mcp.New(reg, mcpOpts(t))
	require.NoError(t, err)
	res, err := connectMCP(t, srv).CallTool(t.Context(), &sdkmcp.CallToolParams{
		Name:      "sync",
		Arguments: map[string]any{"env": "staging", "fix": true, "target": "api"},
	})
	require.NoError(t, err)
	assert.False(t, res.IsError, toolText(t, res))

	require.NoError(t, cron.Run(t.Context(), reg, "sync",
		[]byte(`{"env":"staging","fix":true,"target":"api"}`), nil))

	want := syncInput{Env: "staging", Target: "api", Fix: true}
	assert.Equal(t, []syncInput{want, want, want}, seen)
}
