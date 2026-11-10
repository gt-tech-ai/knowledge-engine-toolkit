package unit_test

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/dataset"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
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
		"data/tools.yaml": fixtures.BuildRef(func(row *fstest.MapFile) {
			row.Data = []byte("tools:\n  - name: buf\n    pin: v1.73.0\n")
		}),
		textDataPathsYaml: fixtures.BuildRef(func(row *fstest.MapFile) {
			row.Data = []byte("paths:\n  root: .\nlimits: {max: 3}\n")
		}),
		"data/notes.md": fixtures.BuildRef(func(row *fstest.MapFile) {
			row.Data = []byte("tools: ignored\n")
		}),
		"other/tools.yaml": fixtures.BuildRef(func(row *fstest.MapFile) {
			row.Data = []byte("tools: []\n")
		}),
	}
	currentDataset, err := dataset.Load(fsys, textData)
	require.NoError(t, err)

	tools, err := currentDataset.Section[[]datasetTool](textTools)
	require.NoError(t, err)
	assert.Equal(t, []datasetTool{{Name: textBuf, Pin: "v1.73.0"}}, tools)

	limits, err := currentDataset.Section[map[string]int]("limits")
	require.NoError(t, err)
	assert.Equal(t, map[string]int{textMax: 3}, limits)

	_, err = currentDataset.Section[map[string]int](textNope)
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))

	_, err = currentDataset.Section[struct {
		Root string `yaml:"rooot"`
	}](textPaths)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.Contains(t, err.Error(), "root")

	fsys[textDataMoreYaml] = fixtures.BuildRef(func(row *fstest.MapFile) {
		row.Data = []byte("limits: {max: 4}\n")
	})
	_, err = dataset.Load(fsys, textData)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.Contains(t, err.Error(), `"limits.max"`)
	assert.Contains(t, err.Error(), textDataMoreYaml)
	assert.Contains(t, err.Error(), textDataPathsYaml)

	fsys[textDataMoreYaml] = fixtures.BuildRef(func(row *fstest.MapFile) {
		row.Data = []byte("limits: [\n")
	})
	_, err = dataset.Load(fsys, textData)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.Contains(t, err.Error(), textDataMoreYaml)
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
		"d/a.yaml": fixtures.BuildRef(func(row *fstest.MapFile) {
			row.Data = []byte("check:\n  ratchet: {root: cli}\ntools:\n" +
				"  - name: buf\n    pin: v1\n")
		}),
		"d/b.yaml": fixtures.BuildRef(func(row *fstest.MapFile) {
			row.Data = []byte("check:\n  ratchet: {hard: x}\n  docs: {on: true}\n" +
				"tools:\n  - name: helm\n    pin: v3\n  - name: helm\n    pin: v3\n")
		}),
	}
	currentDataset, err := dataset.Load(fsys, textDLower)
	require.NoError(t, err)

	check, err := currentDataset.Section[map[string]map[string]any](textCheck)
	require.NoError(t, err)
	assert.Equal(t, map[string]map[string]any{
		"ratchet":     {"root": "cli", "hard": textX},
		textDocsLower: {"on": true},
	}, check)

	tools, err := currentDataset.Section[[]datasetTool](textTools)
	require.NoError(t, err)
	assert.Equal(t, []datasetTool{
		{
			Name: textBuf,
			Pin:  textV1,
		},
		{Name: textHelm, Pin: "v3"},
		{Name: textHelm, Pin: "v3"},
	}, tools)

	conflicts := map[string]struct{ c, path string }{
		textScalar:   {"check:\n  ratchet: {root: other}\n", `"check.ratchet.root"`},
		"named item": {"tools:\n  - name: buf\n    pin: v2\n", `"tools[buf]"`},
		textKind:     {"check:\n  docs: [on]\n", `"check.docs"`},
	}
	for name, testCase := range conflicts {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			mapFS := fstest.MapFS{
				"d/a.yaml": fsys["d/a.yaml"],
				"d/b.yaml": fsys["d/b.yaml"],
				"d/c.yaml": fixtures.BuildRef(func(row *fstest.MapFile) {
					row.Data = []byte(testCase.c)
				}),
			}
			_, err := dataset.Load(mapFS, textDLower)
			assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
			assert.Contains(t, err.Error(), testCase.path)
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
		"cfg/a.yaml": fixtures.BuildRef(func(row *fstest.MapFile) {
			row.Data = []byte("tools:\n  - name: a\n")
		}),
		"cfg/g/b.yaml": fixtures.BuildRef(func(row *fstest.MapFile) {
			row.Data = []byte("tools:\n  - name: b\n")
		}),
		"cfg/z.yaml": fixtures.BuildRef(func(row *fstest.MapFile) {
			row.Data = []byte("tools:\n  - name: z\n")
		}),
		"cfg/templates/t.yaml": fixtures.BuildRef(func(row *fstest.MapFile) {
			row.Data = []byte("tools: {{ .Bad }}\n")
		}),
		"cfg/g/notes.md": fixtures.BuildRef(func(row *fstest.MapFile) {
			row.Data = []byte("tools: []\n")
		}),
	}
	d, err := dataset.LoadTree(fsys, "cfg", "templates")
	require.NoError(t, err)

	tools, err := d.Section[[]datasetTool](textTools)
	require.NoError(t, err)
	assert.Equal(
		t,
		[]datasetTool{
			{Name: textA, Pin: ""},
			{Name: textB, Pin: ""},
			{Name: "z", Pin: ""},
		},
		tools,
	)

	_, err = dataset.LoadTree(fsys, textNope)
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))
}

const (
	// textDataMoreYaml is the literal "data/more.yaml".
	textDataMoreYaml = "data/more.yaml"
)
