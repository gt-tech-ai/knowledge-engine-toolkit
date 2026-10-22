package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// TestCoreTypesFileSelector_IsZero tests which selectors count as unset.
//
// Why this test is important:
//   - A tool run with unset inputs is never cached, and an unset selector is dropped from
//     JSON; a selector that only narrows to tracked files, or only keeps generated
//     files, is a configured selection and must be neither.
//
// What it tests:
//   - The zero selector is zero; setting any one field, the two flags included, is not.
func TestCoreTypesFileSelector_IsZero(t *testing.T) {
	t.Parallel()
	assert.True(t, types.FileSelector{}.IsZero())
	for name, s := range map[string]types.FileSelector{
		"roots":             {Roots: []string{"a"}},
		"paths":             {Paths: []string{"a.go"}},
		"globs":             {Globs: []string{"*.go"}},
		"exts":              {Exts: []string{".go"}},
		"exclude":           {Exclude: []string{"vendor"}},
		"tracked only":      {TrackedOnly: true},
		"include generated": {IncludeGenerated: true},
	} {
		assert.False(t, s.IsZero(), name)
	}
}

// TestCoreTypesText_Lines tests splitting a file into lines.
//
// Why this test is important:
//   - Line-based checks report `file:line` findings and compare line text; a file saved
//     with Windows line endings must split into the same lines as its Unix twin, with no
//     stray carriage return on the last one.
//
// What it tests:
//   - LF and CRLF text with and without a final newline split into the same lines; an
//     empty file has none.
func TestCoreTypesText_Lines(t *testing.T) {
	t.Parallel()
	for _, src := range []string{"a\nb\n", "a\nb", "a\r\nb\r\n", "a\r\nb"} {
		assert.Equal(
			t,
			[]string{"a", "b"},
			types.Text{Bytes: []byte(src)}.Lines(),
			"%q",
			src,
		)
	}
	assert.Empty(t, types.Text{}.Lines())
}
