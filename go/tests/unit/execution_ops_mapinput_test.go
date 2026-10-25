package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/ops"
)

// TestExecutionOps_MapInputDefaults tests the input a map operation runs with.
//
// Why this test is important:
//   - an MCP caller sends only the arguments it sets, while a procedure's expressions
//     read every input the command declares; a flag the caller omits must reach the run
//     as its declared default (or its type's zero), never as a missing key.
//
// What it tests:
//   - an omitted flag is its default, else its type's zero (false, "", 0, an empty list,
//     a zero duration); an omitted argument is "" and an omitted rest is an empty list; a
//     sent value is kept, a JSON number reaches an int or duration flag as that type and
//     a JSON list a strings flag as []string; the schema still names every key.
func TestExecutionOps_MapInputDefaults(t *testing.T) {
	t.Parallel()
	var got map[string]any
	b := ops.MapInput([]types.FlagSpec{
		{Name: "dry-run", Type: types.FlagBool},
		{Name: "profile", Type: types.FlagString, Default: "base"},
		{Name: "seed", Type: types.FlagInt},
		{Name: "only", Type: types.FlagStrings},
		{Name: "wait", Type: types.FlagDuration},
	}, types.ArgSpec{Names: []string{"customer"}, Rest: "cmd"}, func(
		_ context.Context, in map[string]any, _ struct{}, _ interfaces.ExecutionObserver,
	) error {
		got = in
		return nil
	})
	op := b(types.OperationDescriptor{Name: "x"}, struct{}{})
	require.NoError(t, op.Run(t.Context(), &map[string]any{"seed": 7}, nil))
	assert.Equal(t, map[string]any{
		"dry_run": false, "profile": "base", "seed": 7, "only": []string{},
		"wait": int64(0), "customer": "", "cmd": []string{},
	}, got)
	assert.Contains(t, string(op.InputSchema()), `"dry_run"`)

	require.NoError(t, op.Run(t.Context(), &map[string]any{
		"seed": 7.0, "wait": 1.5e9, "only": []any{"a", "b"}, "profile": "x",
	}, nil))
	assert.Equal(t, 7, got["seed"])
	assert.Equal(t, int64(1500000000), got["wait"])
	assert.Equal(t, []string{"a", "b"}, got["only"])
	assert.Equal(t, "x", got["profile"])
}
