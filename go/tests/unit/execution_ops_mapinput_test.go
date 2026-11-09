package unit_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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

	builder := ops.MapInput([]types.FlagSpec{
		{Name: "dry-run", Type: types.FlagBool},
		{Name: textProfile, Type: types.FlagString, Default: textBase},
		{Name: textSeed, Type: types.FlagInt},
		{Name: textOnly, Type: types.FlagStrings},
		{Name: textWait, Type: types.FlagDuration},
	}, types.ArgSpec{Names: []string{"customer"}, Rest: "cmd"}, func(
		_ context.Context, in map[string]any, _ struct{}, _ interfaces.ExecutionObserver,
	) error {
		got = in

		return nil
	})
	operation := builder(types.OperationDescriptor{Name: textX}, struct{}{})
	require.NoError(t, operation.Run(t.Context(), &map[string]any{textSeed: 7}, nil))
	assert.Equal(t, map[string]any{
		"dry_run": false, textProfile: textBase, textSeed: 7, textOnly: []string{},
		textWait: time.Duration(0), "customer": "", "cmd": []string{},
	}, got)
	assert.Contains(t, string(operation.InputSchema()), `"dry_run"`)

	require.NoError(t, operation.Run(t.Context(), &map[string]any{
		textSeed: 7.0, textWait: 1.5e9, textOnly: []any{textA, textB}, textProfile: textX,
	}, nil))
	assert.Equal(t, wantGot7, got[textSeed])
	assert.Equal(t, 1500*time.Millisecond, got[textWait])
	assert.Equal(t, "1.5s", fmt.Sprint(got[textWait]))
	assert.Equal(t, []string{textA, textB}, got[textOnly])
	assert.Equal(t, textX, got[textProfile])
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

	operation := ops.MapInput([]types.FlagSpec{
		{Name: textClientID, Type: types.FlagString},
		{Name: textClientSecret3, Type: types.FlagString, Secret: true},
	}, types.ArgSpec{Names: []string{textEnv}}, func(
		context.Context, map[string]any, struct{}, interfaces.ExecutionObserver,
	) error {
		return nil
	})(types.OperationDescriptor{Name: "kb"}, struct{}{})

	schema := string(operation.InputSchema())
	assert.Contains(t, schema, `"client_id"`)
	assert.NotContains(t, schema, textClientSecret)

	_, err := ops.Decode(operation, []byte(`{"client_id":"a","env":"e"}`))
	require.NoError(t, err)

	for _, raw := range []string{textClientSecretS, `{"bogus":1}`} {
		_, err = ops.Decode(operation, []byte(raw))
		require.Error(t, err, raw)
		assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), err.Error())
	}

	_, err = ops.Decode(operation, []byte(textClientSecretS))
	assert.Contains(t, err.Error(), textClientSecret)

	in, err := ops.DecodeLocal(operation, []byte(textClientSecretS))
	require.NoError(t, err)
	assert.Equal(t, &map[string]any{textClientSecret: textS}, in)

	_, err = ops.DecodeLocal(operation, []byte(`{"bogus":1}`))
	require.Error(t, err)
}
