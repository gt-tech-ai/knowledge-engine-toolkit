package unit_test

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/dataset"
)

// decodedData is the whole-dataset target of the Decode tests.
type decodedData struct {
	// Tools is the tools section.
	Tools []datasetTool `yaml:"tools"`

	// Paths is the paths section.
	Paths struct {
		// Root is the repository root.
		Root string `yaml:"root"`
	} `yaml:"paths"`

	// Limits is the untagged limits section, matched by its lower-cased name.
	Limits map[string]int

	// Absent has no section in the data and stays zero.
	Absent []string `yaml:"absent"`
}

// TestDataset_Decode tests decoding a whole dataset into one struct.
//
// Why this test is important:
//   - A front-end with many sections decodes them in one call; an unknown key or field
//     must fail naming its file, as Section does, rather than being dropped.
//
// What it tests:
//   - Sections from two files decode into their tagged (and untagged) fields; a field
//     with no section stays zero; an unknown top-level key and an unknown nested field
//     are INVALID_INPUT naming the file; a non-pointer or non-struct target is
//     INVALID_INPUT.
func TestDataset_Decode(t *testing.T) {
	t.Parallel()
	good := fstest.MapFS{
		"data/tools.yaml": {Data: []byte("tools:\n  - name: buf\n    pin: v1.73.0\n")},
		"data/paths.yaml": {Data: []byte("paths:\n  root: .\nlimits: {max: 3}\n")},
	}
	d, err := dataset.Load(good, "data")
	require.NoError(t, err)

	var out decodedData
	require.NoError(t, d.Decode(&out))
	assert.Equal(t, []datasetTool{{Name: "buf", Pin: "v1.73.0"}}, out.Tools)
	assert.Equal(t, ".", out.Paths.Root)
	assert.Equal(t, map[string]int{"max": 3}, out.Limits)
	assert.Nil(t, out.Absent)

	unknownKey, err := dataset.Load(fstest.MapFS{
		"data/extra.yaml": {Data: []byte("mystery: 1\n")},
	}, "data")
	require.NoError(t, err)
	err = unknownKey.Decode(&decodedData{})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	require.ErrorContains(t, err, "mystery")
	require.ErrorContains(t, err, "data/extra.yaml")

	unknownField, err := dataset.Load(fstest.MapFS{
		"data/paths.yaml": {Data: []byte("paths:\n  rooot: .\n")},
	}, "data")
	require.NoError(t, err)
	err = unknownField.Decode(&decodedData{})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	require.ErrorContains(t, err, "data/paths.yaml")

	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(d.Decode(decodedData{})))
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(d.Decode((*decodedData)(nil))))
	var notStruct map[string]any
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(d.Decode(&notStruct)))
}

// TestDataset_DecodeKeepsBlockScalars tests that a section decodes to the same values a
// plain yaml.Unmarshal of its file gives.
//
// Why this test is important:
//   - Decoding re-encodes each section; a folded scalar whose lines are more indented
//     (a multi-line query) re-folds into a different string, so the loaded data silently
//     differs from the file.
//
// What it tests:
//   - Folded (with more-indented lines), literal, quoted and plain scalars decode through
//     Section and Decode exactly as yaml.Unmarshal decodes them.
func TestDataset_DecodeKeepsBlockScalars(t *testing.T) {
	t.Parallel()
	src := "q:\n" +
		"  folded: >-\n    (\n      sum(rate(x[2m]))\n      >= 0\n    ) or vector(0)\n" +
		"  literal: |\n    a\n      b\n" +
		"  quoted: \"123\"\n" +
		"  plain: 7\n"
	type queries struct {
		Folded  string `yaml:"folded"`
		Literal string `yaml:"literal"`
		Quoted  string `yaml:"quoted"`
		Plain   int    `yaml:"plain"`
	}
	var want struct {
		Q queries `yaml:"q"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(src), &want))

	ds, err := dataset.Load(fstest.MapFS{"data/q.yaml": {Data: []byte(src)}}, "data")
	require.NoError(t, err)
	got, err := ds.Section[queries]("q")
	require.NoError(t, err)
	assert.Equal(t, want.Q, got)

	var all struct {
		Q queries `yaml:"q"`
	}
	require.NoError(t, ds.Decode(&all))
	assert.Equal(t, want.Q, all.Q)
}
