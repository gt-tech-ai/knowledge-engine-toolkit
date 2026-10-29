package unit_test

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/dataset"
)

// datasetTool is one row of the test dataset's tools section.
type datasetTool struct {
	// Name names the tool.
	Name string `yaml:"name"`

	// Pin is the tool's version.
	Pin string `yaml:"pin"`
}

// TestFoundationDataset_LoadAndSection tests merge-loading layered data files.
//
// Why this test is important:
//   - A front-end's repository data is split across files by topic and decoded into
//     typed sections; a key defined in two files, or a misspelled field, must fail
//     loudly instead of one silently winning or being dropped.
//
// What it tests:
//   - Every *.yaml file in the directory (and none outside it) is merged by top-level
//     key and a section decodes into its type; a missing section is NOT_FOUND; an
//     unknown field is INVALID_INPUT naming the field; a scalar set in two files is
//     INVALID_INPUT naming its key path and both files; a malformed file is
//     INVALID_INPUT naming it.
func TestFoundationDataset_LoadAndSection(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"data/tools.yaml":  {Data: []byte("tools:\n  - name: buf\n    pin: v1.73.0\n")},
		"data/paths.yaml":  {Data: []byte("paths:\n  root: .\nlimits: {max: 3}\n")},
		"data/notes.md":    {Data: []byte("tools: ignored\n")},
		"other/tools.yaml": {Data: []byte("tools: []\n")},
	}
	d, err := dataset.Load(fsys, "data")
	require.NoError(t, err)

	tools, err := d.Section[[]datasetTool]("tools")
	require.NoError(t, err)
	assert.Equal(t, []datasetTool{{Name: "buf", Pin: "v1.73.0"}}, tools)

	limits, err := d.Section[map[string]int]("limits")
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"max": 3}, limits)

	_, err = d.Section[map[string]int]("nope")
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))

	_, err = d.Section[struct {
		Root string `yaml:"rooot"`
	}]("paths")
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.Contains(t, err.Error(), "root")

	fsys["data/more.yaml"] = &fstest.MapFile{Data: []byte("limits: {max: 4}\n")}
	_, err = dataset.Load(fsys, "data")
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.Contains(t, err.Error(), `"limits.max"`)
	assert.Contains(t, err.Error(), "data/more.yaml")
	assert.Contains(t, err.Error(), "data/paths.yaml")

	fsys["data/more.yaml"] = &fstest.MapFile{Data: []byte("limits: [\n")}
	_, err = dataset.Load(fsys, "data")
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.Contains(t, err.Error(), "data/more.yaml")
}

// TestFoundationDataset_MergesAcrossFiles tests that files are slices of one document.
//
// Why this test is important:
//   - A front-end groups its data by topic (one file per verb, a guard's rows beside its
//     params), so two files must be able to add to the same section: maps merge and
//     lists concatenate. Only a value set twice is a conflict, and it must name both
//     files and the key, or the author can't find the clash.
//
// What it tests:
//   - Two files' maps merge key by key, deeply; two files' lists concatenate in file
//     order.
//   - A scalar set in two files, a named list item in two files, and a map in one file
//     against a list in another are each INVALID_INPUT naming the key path and both
//     files; a named item repeated within one file is not a conflict.
func TestFoundationDataset_MergesAcrossFiles(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"d/a.yaml": {Data: []byte("check:\n  ratchet: {root: cli}\ntools:\n" +
			"  - name: buf\n    pin: v1\n")},
		"d/b.yaml": {Data: []byte("check:\n  ratchet: {hard: x}\n  docs: {on: true}\n" +
			"tools:\n  - name: helm\n    pin: v3\n  - name: helm\n    pin: v3\n")},
	}
	d, err := dataset.Load(fsys, "d")
	require.NoError(t, err)

	check, err := d.Section[map[string]map[string]any]("check")
	require.NoError(t, err)
	assert.Equal(t, map[string]map[string]any{
		"ratchet": {"root": "cli", "hard": "x"},
		"docs":    {"on": true},
	}, check)

	tools, err := d.Section[[]datasetTool]("tools")
	require.NoError(t, err)
	assert.Equal(t, []datasetTool{
		{Name: "buf", Pin: "v1"}, {Name: "helm", Pin: "v3"}, {Name: "helm", Pin: "v3"},
	}, tools)

	conflicts := map[string]struct{ c, path string }{
		"scalar":     {"check:\n  ratchet: {root: other}\n", `"check.ratchet.root"`},
		"named item": {"tools:\n  - name: buf\n    pin: v2\n", `"tools[buf]"`},
		"kind":       {"check:\n  docs: [on]\n", `"check.docs"`},
	}
	for name, tc := range conflicts {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fs := fstest.MapFS{
				"d/a.yaml": fsys["d/a.yaml"], "d/b.yaml": fsys["d/b.yaml"],
				"d/c.yaml": {Data: []byte(tc.c)},
			}
			_, err := dataset.Load(fs, "d")
			assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
			assert.Contains(t, err.Error(), tc.path)
			assert.Contains(t, err.Error(), "d/c.yaml")
			assert.Contains(t, err.Error(), "d/a.yaml")
		})
	}
}

// TestFoundationDataset_LoadTree tests loading a directory tree of data files.
//
// Why this test is important:
//   - Grouped data lives in subdirectories (guards/, platform/), while template and
//     scaffold trees beside it hold YAML that is not data; reading the tree in a fixed
//     order keeps list concatenation deterministic, and skipping must keep the
//     templates out.
//
// What it tests:
//   - Every *.yaml under the root and its subdirectories merges, in lexical path order
//     (a.yaml, then g/b.yaml, then z.yaml); a skipped subdirectory is not read; a
//     missing root is NOT_FOUND.
func TestFoundationDataset_LoadTree(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"cfg/a.yaml":           {Data: []byte("tools:\n  - name: a\n")},
		"cfg/g/b.yaml":         {Data: []byte("tools:\n  - name: b\n")},
		"cfg/z.yaml":           {Data: []byte("tools:\n  - name: z\n")},
		"cfg/templates/t.yaml": {Data: []byte("tools: {{ .Bad }}\n")},
		"cfg/g/notes.md":       {Data: []byte("tools: []\n")},
	}
	d, err := dataset.LoadTree(fsys, "cfg", "templates")
	require.NoError(t, err)
	tools, err := d.Section[[]datasetTool]("tools")
	require.NoError(t, err)
	assert.Equal(t, []datasetTool{{Name: "a"}, {Name: "b"}, {Name: "z"}}, tools)

	_, err = dataset.LoadTree(fsys, "nope")
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))
}
