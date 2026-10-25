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
		{Name: "coverage-threshold", Type: types.FlagInt, Default: 80},
		{Name: "go", Type: types.FlagBool},
		{Name: "changed", Type: types.FlagStrings},
		{Name: "env", Type: types.FlagString},
	}
	args := types.ArgSpec{Names: []string{"overlay"}, Rest: "paths"}

	in := map[string]any{"go": true, "env": "staging", "coverage_threshold": 40.0}
	ops.FillInput(flags, args, in)

	assert.Equal(t, map[string]any{
		"coverage_threshold": 40,
		"go":                 true,
		"changed":            []string{},
		"env":                "staging",
		"overlay":            "",
		"paths":              []string{},
	}, in)
}
