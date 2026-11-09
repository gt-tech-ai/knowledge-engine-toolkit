package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// TestBuild_SetsOnlyTheAssignedFields tests the table-row builder.
//
// Why this test is important:
//   - Table rows across the suite are written with Build; a row that lost an assignment
//     or kept stale state would make those tests assert the wrong input.
//
// What it tests:
//   - Build and BuildRef return a value with the assigned field set and the others zero.
func TestBuild_SetsOnlyTheAssignedFields(t *testing.T) {
	t.Parallel()

	type row struct {
		Name string
		Size int
	}

	byValue := fixtures.Build(func(target *row) { target.Name = "a" })
	byRef := fixtures.BuildRef(func(target *row) { target.Size = 7 })

	assert.Equal(t, "a", byValue.Name)
	assert.Zero(t, byValue.Size)
	assert.Empty(t, byRef.Name)
	assert.Equal(t, wantSize7, byRef.Size)
}
