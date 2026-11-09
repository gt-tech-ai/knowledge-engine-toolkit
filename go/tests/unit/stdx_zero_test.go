package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
)

// TestZero_ReturnsTheZeroValue tests the generic zero value.
//
// Why this test is important:
//   - Production code returns stdx.Zero for "nothing found"; a non-zero result would
//     be read as a real value.
//
// What it tests:
//   - Zero returns the zero value for a struct, a pointer and a map.
func TestZero_ReturnsTheZeroValue(t *testing.T) {
	t.Parallel()

	type sample struct {
		Name string
		Size int
	}

	assert.Equal(t, sample{Name: "", Size: 0}, stdx.Zero[sample]())
	assert.Nil(t, stdx.Zero[*sample]())
	assert.Nil(t, stdx.Zero[map[string]int]())
}
