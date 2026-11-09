package unit_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/ops"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
)

// adapterInput is the input of the adapter tests' operation.
type adapterInput struct {
	// Target names what to run on.
	Target string `json:"target" jsonschema:"required"`

	// Hidden is injected by the front-end, never decoded.
	Hidden string `json:"-"`

	// Fix applies fixes.
	Fix bool `json:"fix,omitempty"`
}

// TestExecutionOpsAdapters_FuncDecodeAndDefaults tests the Func adapter.
//
// Why this test is important:
//   - Every front-end decodes its arguments into the operation's own input and an
//     agent reads the schema to call it; an unknown argument silently dropped, a run
//     that mutates the caller's input, or a default lost on decode are each wrong runs.
//
// What it tests:
//   - The schema is a closed object schema of the json fields with the required field
//     and no $schema; Decode fills a fresh input, rejects an unknown field as
//     INVALID_INPUT naming the operation, and leaves WithDefaults' seeds for omitted
//     fields; Run passes a copy of the input and the environment; an input of another
//     type is INVALID_INPUT.
func TestExecutionOpsAdapters_FuncDecodeAndDefaults(t *testing.T) {
	t.Parallel()

	var (
		got    adapterInput
		gotEnv opsEnv
	)

	b := ops.WithDefaults(ops.Func(func(
		_ context.Context, in *adapterInput, env opsEnv, _ interfaces.ExecutionObserver,
	) error {
		in.Target = "mutated"
		got, gotEnv = *in, env

		return nil
	}), func(in *adapterInput) { in.Fix = true })
	op := b(opDesc(textLint, types.SafetyReadOnly, types.OpLifecycleBounded),
		opsEnv{Tag: "cli"})

	var schema map[string]any

	require.NoError(t, json.Unmarshal(op.InputSchema(), &schema))
	assert.Equal(t, "object", schema[textType])
	assert.Equal(t, false, schema["additionalProperties"])
	assert.Equal(t, []any{textTarget}, schema["required"])
	assert.NotContains(t, schema, "$schema")
	assert.Equal(t, []string{textFix, textTarget}, keys(schema["properties"]))

	in, err := ops.Decode(op, []byte(`{"target": "a"}`))
	require.NoError(t, err)
	assert.Equal(t, &adapterInput{Target: textA, Fix: true}, in)

	empty, err := ops.Decode(op, nil)
	require.NoError(t, err)
	assert.Equal(t, &adapterInput{Fix: true}, empty)

	_, err = ops.Decode(op, []byte(`{"targt": "a"}`))
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.Contains(t, err.Error(), `"lint"`)

	require.NoError(t, op.Run(t.Context(), in, nil))
	assert.Equal(t, adapterInput{Target: "mutated", Fix: true}, got)
	assert.Equal(
		t,
		textA,
		asType[*adapterInput](t, in).Target,
		"the caller's input is not mutated",
	)
	assert.Equal(t, "cli", gotEnv.Tag)

	err = op.Run(t.Context(), textNope, nil)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// keys returns the sorted keys of a decoded JSON object.
func keys(v any) []string {
	m := stdx.As[map[string]any](v)
	out := make([]string, 0, len(m))

	for k := range m {
		out = append(out, k)
	}

	slices.Sort(out)

	return out
}
