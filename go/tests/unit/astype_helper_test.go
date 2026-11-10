package unit_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// asType returns v as a T and fails the test when v holds anything else, so a test
// that reads a decoded or mocked value never panics on a wrong type.
func asType[T any](tb testing.TB, v any) T {
	tb.Helper()

	got, ok := v.(T)
	require.True(tb, ok, "want %T, got %T", got, v)

	return got
}
