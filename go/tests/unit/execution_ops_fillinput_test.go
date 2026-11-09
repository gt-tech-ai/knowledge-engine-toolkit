package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/ops"
)

// TestOps_FillInput tests that FillInput completes a partial input the way the MapInput
// operation does for its own front-ends.
//
// Why this test is important:
//   - A composite that runs an operation's procedure directly (preflight's test members)
//     bypasses the MapInput operation, so the rows would read an unbound variable for
//     every input the caller omitted — a CEL `!go && !python` over an absent `python`
//     fails the run. Filling through the same function keeps the two paths identical.
//
// What it tests:
//   - An omitted flag gets its declared default, else its type's zero; an omitted
//     argument gets ""; the rest argument gets no items; a sent JSON number is coerced
//     to the flag's int type and a sent value is otherwise kept.
func TestOps_FillInput(t *testing.T) {
	t.Parallel()

	flags := []types.FlagSpec{
		{Name: "coverage-threshold", Type: types.FlagInt, Default: default80},
		{Name: textGo2, Type: types.FlagBool},
		{Name: textChanged, Type: types.FlagStrings},
		{Name: textEnv, Type: types.FlagString},
	}
	args := types.ArgSpec{Names: []string{"overlay"}, Rest: textPaths}

	in := map[string]any{
		textGo2:              true,
		textEnv:              textStaging,
		"coverage_threshold": coverageThreshold40p0,
	}
	ops.FillInput(flags, args, in)

	assert.Equal(t, map[string]any{
		"coverage_threshold": 40,
		textGo2:              true,
		textChanged:          []string{},
		textEnv:              textStaging,
		"overlay":            "",
		textPaths:            []string{},
	}, in)
}
