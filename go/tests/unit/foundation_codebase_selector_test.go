package unit_test

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/codebase"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// TestCodebaseSelector_Matches tests how each FileSelector field narrows a selection.
//
// Why this test is important:
//   - Every check, tool run and generator names its files with a selector; a field that
//     widened instead of narrowed, or an exclusion a matching glob could override, would
//     scan files a check means to skip or skip files it must cover.
//
// What it tests:
//   - The zero selector matches everything; Roots, Paths, Globs and Exts each narrow;
//     the fields combine with AND; an Exclude glob or directory beats a matching glob.
func TestCodebaseSelector_Matches(t *testing.T) {
	t.Parallel()

	testCodebaseSelectorMatchesCases(t)
}

// selectorCase is one path-against-selector expectation.
type selectorCase struct {
	name string
	path string
	sel  types.FileSelector
	want bool
}

// testCodebaseSelectorMatchesCases continues TestCodebaseSelector_Matches.
func testCodebaseSelectorMatchesCases(t *testing.T) {
	t.Helper()

	cases := slices.Concat(
		selectorRootAndPathCases(),
		selectorGlobCases(),
		selectorExtAndExcludeCases(),
	)
	for index := range cases {
		testCase := &cases[index]
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, testCase.want, codebase.Matches(&testCase.sel, testCase.path))
		})
	}
}

// selectorRootAndPathCases returns the selector cases over roots and exact paths.
func selectorRootAndPathCases() []selectorCase {
	return []selectorCase{
		{"zero selects all", "a/b/c.txt", stdx.Zero[types.FileSelector](), true},
		{
			"root keeps under",
			"a/b/c.txt",
			selectRoots(textA),
			true,
		},
		{
			"root drops outside",
			"ab/c.txt",
			selectRoots(textA),
			false,
		},
		{
			"dot root keeps all",
			"x.txt",
			selectRoots("."),
			true,
		},
		{
			"paths exact",
			textABGo,
			selectPaths(textABGo),
			true,
		},
	}
}

// selectorGlobCases returns the selector cases over globs.
func selectorGlobCases() []selectorCase {
	return []selectorCase{
		{
			"paths drop other",
			"a/c.go",
			selectPaths(textABGo),
			false,
		},
		{
			"glob star segment",
			textABGo,
			selectGlobs("a/*.go"),
			true,
		},
		{
			"glob star one level",
			"a/x/b.go",
			selectGlobs("a/*.go"),
			false,
		},
		{
			"glob doublestar deep",
			"a/x/y/b.go",
			selectGlobs("a/**/*.go"),
			true,
		},
		{
			"glob doublestar zero",
			textABGo,
			selectGlobs("a/**/*.go"),
			true,
		},
	}
}

// selectorExtAndExcludeCases returns the selector cases over extensions and exclusions.
func selectorExtAndExcludeCases() []selectorCase {
	return []selectorCase{
		{
			"glob leading doublestar",
			"k/s/values-dev.yaml",
			selectGlobs("**/values*.yaml"),
			true,
		},
		{
			"ext keeps",
			"a/b.yml",
			selectExts(textYaml, ".yml"),
			true,
		},
		{
			"ext drops",
			textABGo,
			selectExts(textYaml),
			false,
		},
		{
			"and of fields",
			textBCGo,
			selectorWith(func(row *types.FileSelector) {
				row.Roots = []string{textA}
				row.Exts = []string{textGoExt}
			}),
			false,
		},
		{
			"exclude dir beats glob",
			"a/gen/x.go",
			selectorWith(func(row *types.FileSelector) {
				row.Globs = []string{"**/*.go"}
				row.Exclude = []string{"a/gen"}
			}),
			false,
		},
		{
			"exclude glob",
			"a/b_test.go",
			selectExclude("**/*_test.go"),
			false,
		},
		{
			"exclude keeps sibling",
			"a/generic.go",
			selectExclude("a/gen"),
			true,
		},
	}
}

// selectRoots returns a selector over roots.
func selectRoots(roots ...string) types.FileSelector {
	return selectorWith(func(row *types.FileSelector) { row.Roots = roots })
}

// selectPaths returns a selector over exact paths.
func selectPaths(paths ...string) types.FileSelector {
	return selectorWith(func(row *types.FileSelector) { row.Paths = paths })
}

// selectGlobs returns a selector over globs.
func selectGlobs(globs ...string) types.FileSelector {
	return selectorWith(func(row *types.FileSelector) { row.Globs = globs })
}

// selectExts returns a selector over extensions.
func selectExts(exts ...string) types.FileSelector {
	return selectorWith(func(row *types.FileSelector) { row.Exts = exts })
}

// selectExclude returns a selector with only exclusions.
func selectExclude(exclude ...string) types.FileSelector {
	return selectorWith(func(row *types.FileSelector) { row.Exclude = exclude })
}

// selectorWith returns a file selector with only the fields set sets.
func selectorWith(set func(row *types.FileSelector)) types.FileSelector {
	return fixtures.Build(set)
}

// TestCodebaseSelector_Generated tests how generated Go files are recognized.
//
// Why this test is important:
//   - Checks skip generated code by default; a missed marker makes them report findings
//     nobody may edit, and a false positive hides hand-written code from them.
//
// What it tests:
//   - The standard header before the package clause marks a file generated; the same
//     text after the package clause does not; generated names are recognized.
func TestCodebaseSelector_Generated(t *testing.T) {
	t.Parallel()
	assert.True(t, codebase.GeneratedHeader([]byte(
		"// Code generated by mockgen. DO NOT EDIT.\n\npackage mocks\n")))
	assert.False(t, codebase.GeneratedHeader([]byte(
		"package x\n\n// Code generated by hand. DO NOT EDIT.\n")))
	assert.False(t, codebase.GeneratedHeader([]byte("package x\n")))

	for _, p := range []string{
		"a/b.pb.go", "a/b_gen.go", "zz_generated.deepcopy.go", "m/mock_x.go",
	} {
		assert.True(t, codebase.GeneratedName(p), p)
	}

	assert.False(t, codebase.GeneratedName("a/generator.go"))
}

const (
	// textABGo is the literal "a/b.go".
	textABGo = "a/b.go"
)
