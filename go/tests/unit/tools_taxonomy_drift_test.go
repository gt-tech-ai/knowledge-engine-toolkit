package unit_test

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	fscodebase "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/codebase/fs"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/codebase"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/gen"
)

// repoRoot is the repository root relative to this package.
const repoRoot = "../../.."

// taxonomyGenerator is a generator that renders one taxonomy YAML into a Go and a
// Python output, with the drift checks every such generator shares.
type taxonomyGenerator struct {
	// newGenerator builds the generator.
	newGenerator func() interfaces.Generator

	// source is the taxonomy's repository-relative path.
	source string

	// goPath is the rendered Go file's repository-relative path.
	goPath string

	// pyPath is the rendered Python module's repository-relative path.
	pyPath string

	// regenerate is the command that refreshes the outputs, named in a failure.
	regenerate string
}

// handEdit is one hand edit of a generated output that must read as drift.
type handEdit struct {
	// name names the case.
	name string

	// path is the edited output.
	path string

	// from is the text replaced.
	from string

	// to is its replacement.
	to string
}

// committed returns the committed taxonomy source and both rendered outputs, keyed by
// their repository-relative paths.
func (g taxonomyGenerator) committed(t *testing.T) map[string]string {
	t.Helper()

	files := map[string]string{}

	for _, path := range []string{g.source, g.goPath, g.pyPath} {
		b, err := os.ReadFile(filepath.FromSlash(repoRoot + textSlash + path))
		require.NoError(t, err)

		files[path] = string(b)
	}

	return files
}

// requireInSync asserts the generator renders exactly its two outputs and that the
// committed ones are a fresh render.
func (g taxonomyGenerator) requireInSync(t *testing.T) {
	t.Helper()

	currentCodebase := codebase.New(
		fscodebase.New(filepath.FromSlash(repoRoot)),
		codebase.NewSnapshot(g.newGenerator().Name()),
		nil,
	)

	out, err := g.newGenerator().Render(t.Context(), currentCodebase)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{g.goPath, g.pyPath}, slices.Collect(maps.Keys(out)))

	drift, err := gen.Check(t.Context(), currentCodebase, g.newGenerator())

	require.NoError(t, err)
	require.True(t, drift.InSync, "run `%s`:\n%s", g.regenerate, drift.Diff)
}

// requireHandEditsDrift asserts each edit, applied to the committed files, makes
// gen.Check report exactly the edited path out of sync.
func (g taxonomyGenerator) requireHandEditsDrift(t *testing.T, edits []handEdit) {
	t.Helper()

	for _, edit := range edits {
		t.Run(edit.name, func(t *testing.T) {
			t.Parallel()

			files := g.committed(t)
			require.Contains(t, files[edit.path], edit.from)

			files[edit.path] = strings.Replace(files[edit.path], edit.from, edit.to, 1)

			drift, err := gen.Check(
				t.Context(),
				memoryCodebase(t, files),
				g.newGenerator(),
			)

			require.NoError(t, err)
			require.False(t, drift.InSync)
			require.Equal(t, []string{edit.path}, drift.Paths)
		})
	}
}
