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
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
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
		fixtures.Build(func(row *types.FlagSpec) {
			row.Name = "dry-run"
			row.Type = types.FlagBool
		}),
		fixtures.Build(func(row *types.FlagSpec) {
			row.Name = textProfile
			row.Type = types.FlagString
			row.Default = textBase
		}),
		fixtures.Build(func(row *types.FlagSpec) {
			row.Name = textSeed
			row.Type = types.FlagInt
		}),
		fixtures.Build(func(row *types.FlagSpec) {
			row.Name = textOnly
			row.Type = types.FlagStrings
		}),
		fixtures.Build(func(row *types.FlagSpec) {
			row.Name = textWait
			row.Type = types.FlagDuration
		}),
	}, types.ArgSpec{Names: []string{"customer"}, Rest: "cmd", Min: 0}, func(
		_ context.Context, in map[string]any, _ struct{}, _ interfaces.ExecutionObserver,
	) error {
		got = in

		return nil
	})
	operation := builder(
		fixtures.Build(func(row *types.OperationDescriptor) {
			row.Name = textX
		}),
		struct{}{},
	)
	require.NoError(t, operation.Run(t.Context(), &map[string]any{textSeed: 7}, nil))
	assert.Equal(t, map[string]any{
		"dry_run": false, textProfile: textBase, textSeed: 7, textOnly: []string{},
		textWait: time.Duration(0), "customer": "", "cmd": []string{},
	}, got)
	assert.Contains(t, string(operation.InputSchema()), `"dry_run"`)

	require.NoError(t, operation.Run(t.Context(), &map[string]any{
		textSeed: 7.0, textWait: 1.5e9, textOnly: []any{textA, textB}, textProfile: textX,
	}, nil))
	assert.Equal(t, wantGotDefaults, got[textSeed])
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
		fixtures.Build(func(row *types.FlagSpec) {
			row.Name = textClientID
			row.Type = types.FlagString
		}),
		fixtures.Build(func(row *types.FlagSpec) {
			row.Name = textClientSecretKebab
			row.Type = types.FlagString
			row.Secret = true
		}),
	}, types.ArgSpec{Names: []string{textEnv}, Rest: "", Min: 0}, func(
		context.Context, map[string]any, struct{}, interfaces.ExecutionObserver,
	) error {
		return nil
	})(fixtures.Build(func(row *types.OperationDescriptor) {
		row.Name = "kb"
	}), struct{}{})

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

const (
	// wantGotDefaults is the expected got defaults.
	wantGotDefaults = 7
)

const (
	// textClientSecretS is the literal "{\"client_secret\":\"s\"}".
	textClientSecretS = "{\"client_secret\":\"s\"}"
)
