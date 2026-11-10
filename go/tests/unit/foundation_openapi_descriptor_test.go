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
	text := filepath.Join(dir, "a.swagger.json")
	otherText := filepath.Join(dir, "b.swagger.json")

	require.NoError(t, os.WriteFile(
		text,
		[]byte(`{"info":{"title":"a"},"paths":{"/a":{}}}`),
		writeFileInput,
	))
	require.NoError(t, os.WriteFile(
		otherText,
		[]byte(`{"info":{"title":"b"},"paths":{"/b":{}}}`),
		writeFileInput,
	))

	statusByName, err := openapi.MergeSwaggerFiles(
		fsys.NewOSFS(),
		[]string{text, otherText},
	)
	require.NoError(t, err)

	secondM, err := openapi.MergeSwaggerFiles(fsys.NewOSFS(), []string{otherText, text})
	require.NoError(t, err)

	require.Equal(
		t,
		fmt.Sprint(statusByName[textInfo]),
		fmt.Sprint(secondM[textInfo]),
		"merge base/info is order-dependent: %v vs %v",
		statusByName[textInfo],
		secondM[textInfo],
	)
}
