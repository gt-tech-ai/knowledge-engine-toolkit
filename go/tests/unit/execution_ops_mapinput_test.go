package unit_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
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
//     sent value is kept, a JSON number reaches an int flag as an int and a duration flag
//     as a time.Duration (so a procedure interpolates it as "1.5s", not nanoseconds), and
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
		"wait": time.Duration(0), "customer": "", "cmd": []string{},
	}, got)
	assert.Contains(t, string(op.InputSchema()), `"dry_run"`)

	require.NoError(t, op.Run(t.Context(), &map[string]any{
		"seed": 7.0, "wait": 1.5e9, "only": []any{"a", "b"}, "profile": "x",
	}, nil))
	assert.Equal(t, 7, got["seed"])
	assert.Equal(t, 1500*time.Millisecond, got["wait"])
	assert.Equal(t, "1.5s", fmt.Sprint(got["wait"]))
	assert.Equal(t, []string{"a", "b"}, got["only"])
	assert.Equal(t, "x", got["profile"])
}

// TestExecutionOps_MapInputSecretFlag tests how a map operation's secret flag reaches
// each front-end.
//
// Why this test is important:
//   - A credential passed as a tool argument lands in an agent's transcript, its tool
//     log and the run log; a secret flag must never travel that way, while the CLI, where
//     a human typed it, still takes it.
//   - A map input decodes any JSON object, so before this an undeclared key (a typo, or
//     a secret the schema hides) reached the run unchecked, breaking Decode's
//     "rejects a field the input does not declare" contract.
//
// What it tests:
//   - the input schema omits the secret flag and keeps the others;
//   - Decode rejects the secret key and any other undeclared key (CodeInvalidInput
//     naming the key), and admits the declared ones;
//   - DecodeLocal admits the secret key and still rejects an undeclared one.
func TestExecutionOps_MapInputSecretFlag(t *testing.T) {
	t.Parallel()
	op := ops.MapInput([]types.FlagSpec{
		{Name: "client-id", Type: types.FlagString},
		{Name: "client-secret", Type: types.FlagString, Secret: true},
	}, types.ArgSpec{Names: []string{"env"}}, func(
		context.Context, map[string]any, struct{}, interfaces.ExecutionObserver,
	) error {
		return nil
	})(types.OperationDescriptor{Name: "kb"}, struct{}{})

	schema := string(op.InputSchema())
	assert.Contains(t, schema, `"client_id"`)
	assert.NotContains(t, schema, "client_secret")

	_, err := ops.Decode(op, []byte(`{"client_id":"a","env":"e"}`))
	require.NoError(t, err)
	for _, raw := range []string{`{"client_secret":"s"}`, `{"bogus":1}`} {
		_, err = ops.Decode(op, []byte(raw))
		require.Error(t, err, raw)
		assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), err.Error())
	}
	_, err = ops.Decode(op, []byte(`{"client_secret":"s"}`))
	assert.Contains(t, err.Error(), "client_secret")

	in, err := ops.DecodeLocal(op, []byte(`{"client_secret":"s"}`))
	require.NoError(t, err)
	assert.Equal(t, &map[string]any{"client_secret": "s"}, in)
	_, err = ops.DecodeLocal(op, []byte(`{"bogus":1}`))
	require.Error(t, err)
}
