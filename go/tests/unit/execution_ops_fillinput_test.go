package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/ops"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
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
		fixtures.Build(func(row *types.FlagSpec) {
			row.Name = "coverage-threshold"
			row.Type = types.FlagInt
			row.Default = defaultNumber
		}),
		fixtures.Build(func(row *types.FlagSpec) {
			row.Name = textGoLower
			row.Type = types.FlagBool
		}),
		fixtures.Build(func(row *types.FlagSpec) {
			row.Name = textChanged
			row.Type = types.FlagStrings
		}),
		fixtures.Build(func(row *types.FlagSpec) {
			row.Name = textEnv
			row.Type = types.FlagString
		}),
	}
	args := types.ArgSpec{Names: []string{"overlay"}, Rest: textPaths, Min: 0}

	statusByName := map[string]any{
		textGoLower:          true,
		textEnv:              textStaging,
		"coverage_threshold": wantCoverageThreshold,
	}
	ops.FillInput(flags, args, statusByName)

	assert.Equal(t, map[string]any{
		"coverage_threshold": 40,
		textGoLower:          true,
		textChanged:          []string{},
		textEnv:              textStaging,
		"overlay":            "",
		textPaths:            []string{},
	}, statusByName)
}

const (
	// wantCoverageThreshold is the value for "coverage_threshold" in TestOps_FillInput.
	wantCoverageThreshold = 40.0

	// defaultNumber is the value assigned to row.Default in TestOps_FillInput.
	defaultNumber = 80
)
