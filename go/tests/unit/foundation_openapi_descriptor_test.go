package unit_test

// Black-box tests for the openapi codegen package's descriptor reader and swagger
// merge. They drive the exported openapi.MergeSwaggerFiles, openapi.ReadServiceConfigs,
// and openapi.ValidateResourceKeys — the inspectable entry points over the
// unexported readServiceConfigs/validateResourceKeys/mergeSwaggerFiles — so the
// annotation-decode contract and the ambiguous-key guard are exercised without
// touching any unexported symbol.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/fsys"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/openapi"
)

// TestMergeSwaggerFiles_OrderIndependent verifies the merged spec's base/info is
// deterministic regardless of input file order.
//
// Why this test is important:
//   - The merge previously used the first file as the base verbatim, so a
//     non-deterministic caller order (map-iteration-built file lists) flipped the
//     merged info.title between runs, producing spurious codegen diffs.
//
// What it tests:
//   - MergeSwaggerFiles([a,b]) and MergeSwaggerFiles([b,a]) yield the same info.
func TestMergeSwaggerFiles_OrderIndependent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := filepath.Join(dir, "a.swagger.json")
	b := filepath.Join(dir, "b.swagger.json")
	require.NoError(t, os.WriteFile(
		a,
		[]byte(`{"info":{"title":"a"},"paths":{"/a":{}}}`),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		b,
		[]byte(`{"info":{"title":"b"},"paths":{"/b":{}}}`),
		0o644,
	))

	m1, err := openapi.MergeSwaggerFiles(fsys.NewOSFS(), []string{a, b})
	require.NoError(t, err)
	m2, err := openapi.MergeSwaggerFiles(fsys.NewOSFS(), []string{b, a})
	require.NoError(t, err)

	require.Equal(t, fmt.Sprint(m1["info"]), fmt.Sprint(m2["info"]),
		"merge base/info is order-dependent: %v vs %v", m1["info"], m2["info"])
}
