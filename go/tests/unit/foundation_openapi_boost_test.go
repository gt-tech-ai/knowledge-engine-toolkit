package unit_test

// Additional black-box tests for the openapi codegen package. They drive the public
// openapi.Convert over a synthetic workspace/v1 swagger file (single-resource,
// collection, body/path/query params, error response) built against the real buf
// descriptor, so the Swagger 2.0 → OpenAPI 3 conversion and the JSON:API
// single/collection envelope wrappers are exercised end-to-end without touching
// unexported symbols.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/fsys"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/openapi"
)

// TestMergeSwaggerFiles_MergesPathsAndDefinitions tests that MergeSwaggerFiles
// unions paths and definitions across files and surfaces read/parse errors.
//
// Why this test is important:
//   - Multiple proto packages map to one service; their swagger files must merge
//     into one spec without dropping paths or definitions, and a corrupt file must
//     fail loudly.
//
// What it tests:
//   - Two files merge their paths and definitions; a missing file errors; a
//     malformed-JSON file errors.
func TestMergeSwaggerFiles_MergesPathsAndDefinitions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := filepath.Join(dir, "a.swagger.json")
	b := filepath.Join(dir, "b.swagger.json")
	require.NoError(t, os.WriteFile(a, []byte(
		`{"info":{"title":"base"},"paths":{"/a":{}},"definitions":{"A":{"type":"object"}}}`,
	), 0o644))
	require.NoError(t, os.WriteFile(b, []byte(
		`{"paths":{"/b":{}},"definitions":{"B":{"type":"object"}}}`,
	), 0o644))

	merged, err := openapi.MergeSwaggerFiles(fsys.NewOSFS(), []string{a, b})
	require.NoError(t, err)
	paths := openapiAsMap(t, merged["paths"], "paths")
	require.Contains(t, paths, "/a")
	require.Contains(t, paths, "/b")
	defs := openapiAsMap(t, merged["definitions"], "definitions")
	require.Contains(t, defs, "A")
	require.Contains(t, defs, "B")

	_, err = openapi.MergeSwaggerFiles(
		fsys.NewOSFS(),
		[]string{filepath.Join(dir, "missing.json")},
	)
	require.Error(t, err)

	bad := filepath.Join(dir, "bad.swagger.json")
	require.NoError(t, os.WriteFile(bad, []byte("{not json"), 0o644))
	_, err = openapi.MergeSwaggerFiles(fsys.NewOSFS(), []string{bad})
	require.Error(t, err)

	// Base file with neither paths nor definitions, later files add them: exercises
	// the create-on-first-add branches for both maps.
	base := filepath.Join(dir, "0base.swagger.json")
	addP := filepath.Join(dir, "1paths.swagger.json")
	addD := filepath.Join(dir, "2defs.swagger.json")
	require.NoError(t, os.WriteFile(base, []byte(`{"info":{"title":"base"}}`), 0o644))
	require.NoError(t, os.WriteFile(addP, []byte(`{"paths":{"/p":{}}}`), 0o644))
	require.NoError(
		t,
		os.WriteFile(addD, []byte(`{"definitions":{"D":{"type":"object"}}}`), 0o644),
	)
	merged2, err := openapi.MergeSwaggerFiles(fsys.NewOSFS(), []string{base, addP, addD})
	require.NoError(t, err)
	require.Contains(t, openapiAsMap(t, merged2["paths"], "paths"), "/p")
	require.Contains(t, openapiAsMap(t, merged2["definitions"], "defs"), "D")
}

// TestOpenAPIConvert_EmptyAndErrors tests Convert's no-input and bad-descriptor
// paths.
//
// Why this test is important:
//   - An empty input dir is a benign no-op (the loud signal is a failed upstream
//     buf generate), while a missing descriptor must error.
//
// What it tests:
//   - Convert over an empty dir returns an empty result and no error; Convert with a
//     missing descriptor errors.
func TestOpenAPIConvert_EmptyAndErrors(t *testing.T) {
	t.Parallel()
	empty := t.TempDir()
	res, err := openapi.Convert(
		fsys.NewOSFS(),
		empty,
		t.TempDir(),
		filepath.Join(empty, "none.binpb"),
		nil, nil,
	)
	require.NoError(t, err)
	require.Empty(t, res.Written)

	// A swagger file present but a missing descriptor → error.
	root := t.TempDir()
	inRoot := filepath.Join(root, "in")
	require.NoError(t, os.MkdirAll(filepath.Join(inRoot, "workspace", "v1"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(inRoot, "workspace", "v1", "x.swagger.json"),
		[]byte(`{"swagger":"2.0"}`), 0o644,
	))
	_, err = openapi.Convert(fsys.NewOSFS(), inRoot, filepath.Join(root, "out"),
		filepath.Join(root, "missing.binpb"), nil, nil)
	require.Error(t, err)
}
