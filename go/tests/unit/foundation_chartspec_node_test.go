package unit_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/chartspec"
)

// TestNodeHash_GoldenCorpus tests Node.Canonical and Node.Hash against the shared node
// corpus.
//
// Why this test is important:
//   - A stored ChartSpec version is addressed by these hashes; an encoding that drifts
//     (number form, key order, an omitted zero member) silently orphans every stored
//     node, and a second-language hasher must reproduce them byte for byte.
//
// What it tests:
//   - each corpus node encodes to exactly its RFC 8785 canonical bytes (the RFC's number
//     and key-order vectors, unicode keys, zero members and empty lists omitted, hashes
//     as lowercase hex) and hashes to its pinned SHA-256 address.
func TestNodeHash_GoldenCorpus(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(
		filepath.Join(
			textDotDot,
			textDotDot,
			textDotDot,
			"testdata",
			"chartspec",
			"nodes.golden.json",
		),
	)
	require.NoError(t, err)

	// shapeCases is the Cases element of this document.
	type shapeCases struct {
		Node      chartspec.Node `json:"node"`
		Name      string         `json:"name"`
		Canonical string         `json:"canonical"`
		Hash      string         `json:"hash"`
	}

	var corpus struct {
		Cases []shapeCases `json:"cases"`
	}

	require.NoError(t, json.Unmarshal(raw, &corpus))
	require.GreaterOrEqual(t, len(corpus.Cases), minCasesLen)

	for _, currentShapeCases := range corpus.Cases {
		canonical, err := currentShapeCases.Node.Canonical()
		require.NoError(t, err, currentShapeCases.Name)
		assert.Equal(
			t,
			currentShapeCases.Canonical,
			string(canonical),
			currentShapeCases.Name,
		)

		h, err := currentShapeCases.Node.Hash()
		require.NoError(t, err, currentShapeCases.Name)
		assert.Equal(t, currentShapeCases.Hash, h.String(), currentShapeCases.Name)
	}
}

const (
	// minCasesLen is the min cases len the tests use.
	minCasesLen = 6
)
