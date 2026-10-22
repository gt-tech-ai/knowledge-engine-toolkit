package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// TestSplitCode tests that SplitCode separates a coded error's code from its message
// and refuses text that is not coded.
//
// Why this test is important:
//   - A display edge strips the code and a gate recovers it from the same text; if the
//     two parsed differently, a reporter would hide a code the gate acted on, or show
//     an uncoded message with its first word cut off.
//
// What it tests:
//   - "<CODE>: <rest>" splits into the code and the rest, an AppError's own text splits,
//     and text without the separator, with a lower-case prefix, or with an over-long
//     prefix is not ok.
func TestSplitCode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		text, code, rest string
		ok               bool
	}{
		{"UPSTREAM: registry unreachable", "UPSTREAM", "registry unreachable", true},
		{"INVALID_INPUT: a: b", "INVALID_INPUT", "a: b", true},
		{"PATH is unset", "", "", false},
		{"lower: x", "", "", false},
		{"ABCDEFGHIJKLMNOPQRSTUVWXYZ: too long", "", "", false},
		{": empty prefix", "", "", false},
	}
	for _, tc := range cases {
		code, rest, ok := apperr.SplitCode(tc.text)
		assert.Equal(t, tc.ok, ok, tc.text)
		assert.Equal(t, tc.code, code, tc.text)
		assert.Equal(t, tc.rest, rest, tc.text)
	}

	code, rest, ok := apperr.SplitCode(
		apperr.New(apperr.CodeNotFound, "no such tool").Error(),
	)
	assert.True(t, ok)
	assert.Equal(t, string(apperr.CodeNotFound), code)
	assert.Equal(t, "no such tool", rest)
}

// FuzzSplitCode tests that a split always reassembles into its input.
//
// Why this test is important:
//   - SplitCode must never invent or drop text: whatever it returns has to be exactly
//     the input's code and remainder.
//
// What it tests:
//   - Whenever ok, code + ": " + rest equals the input; whenever not ok, both parts are
//     empty.
func FuzzSplitCode(f *testing.F) {
	for _, seed := range []string{"UPSTREAM: x", "a: b", "", "X:", "X: ", "A_1: : :"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		code, rest, ok := apperr.SplitCode(text)
		if !ok {
			assert.Empty(t, code)
			assert.Empty(t, rest)
			return
		}
		assert.Equal(t, text, code+": "+rest)
	})
}
