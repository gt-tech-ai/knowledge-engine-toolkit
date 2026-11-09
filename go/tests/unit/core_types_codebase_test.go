package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
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
	assert.True(t, new(types.FileSelector).IsZero())

	for name, fileSelector := range map[string]types.FileSelector{
		"roots": fixtures.Build(func(row *types.FileSelector) {
			row.Roots = []string{textA}
		}),
		textPaths: fixtures.Build(func(row *types.FileSelector) {
			row.Paths = []string{textAGo}
		}),
		"globs": fixtures.Build(func(row *types.FileSelector) {
			row.Globs = []string{textGo}
		}),
		"exts": fixtures.Build(func(row *types.FileSelector) {
			row.Exts = []string{textGoExt}
		}),
		"exclude": fixtures.Build(func(row *types.FileSelector) {
			row.Exclude = []string{"vendor"}
		}),
		"tracked only": fixtures.Build(func(row *types.FileSelector) {
			row.TrackedOnly = true
		}),
		"include generated": fixtures.Build(func(row *types.FileSelector) {
			row.IncludeGenerated = true
		}),
	} {
		assert.False(t, fileSelector.IsZero(), name)
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
			[]string{textA, textB},
			types.Text{Bytes: []byte(src), Path: "", Binary: false}.Lines(),
			"%q",
			src,
		)
	}

	assert.Empty(t, stdx.Zero[types.Text]().Lines())
}
