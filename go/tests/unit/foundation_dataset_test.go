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
//     unknown field is INVALID_INPUT naming the field; a key in two files is
//     INVALID_INPUT naming both files; a malformed file is INVALID_INPUT naming it.
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
	assert.Contains(t, err.Error(), `"limits"`)
	assert.Contains(t, err.Error(), "data/more.yaml")
	assert.Contains(t, err.Error(), "data/paths.yaml")

	fsys["data/more.yaml"] = &fstest.MapFile{Data: []byte("limits: [\n")}
	_, err = dataset.Load(fsys, "data")
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.Contains(t, err.Error(), "data/more.yaml")
}
