package unit_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/cas"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/chartspec"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
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
//     as lowercase hex) and hashes to its pinned SHA-256 address, which is also what
//     cas.Sum returns for the canonical bytes under the "chartspec/v1" domain.
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

		nodeHash, err := currentShapeCases.Node.Hash()
		require.NoError(t, err, currentShapeCases.Name)
		assert.Equal(
			t,
			currentShapeCases.Hash,
			nodeHash.String(),
			currentShapeCases.Name,
		)
		assert.Equal(
			t,
			cas.Sum("chartspec/v1", canonical).String(),
			nodeHash.String(),
			currentShapeCases.Name,
		)
	}
}

const (
	// minCasesLen is the e2 passed to GreaterOrEqual in TestNodeHash_GoldenCorpus.
	minCasesLen = 6
)

// cellSpecJSON is a spec using the cell mark, a diverging stack and a reduce block.
const cellSpecJSON = `{
  "cube": "genai_calls",
  "rows": {"field": "team"},
  "columns": {"field": "model"},
  "encodings": {"color": {"field": "tokens_in", "agg": "sum"}},
  "mark": "cell",
  "stack": "diverging",
  "reduce": {"percent": "percent_of_total", "min_cell": 5}
}`

// TestChartSpec_StackAndReduceRoundTrip tests the cell mark, stack and reduce members
// through Parse, Wire, Encode and Decode.
//
// Why this test is important:
//   - Saved charts and the interview analytics share one spec; a stack or reduce setting
//     that is dropped on save would change what a reopened chart shows, and one that is
//     present on an old spec would re-address every stored version
//
// What it tests:
//   - the spec parses, its wire form carries mark, stack and reduce, and Decode of the
//     encoded root returns the same spec
//   - a spec without stack or reduce encodes to the same root as before (the golden
//     corpus pins the rest)
//   - an unknown stack, an unknown percent and a negative min_cell are CodeInvalidInput
func TestChartSpec_StackAndReduceRoundTrip(t *testing.T) {
	t.Parallel()

	spec, err := chartspec.Parse([]byte(cellSpecJSON), chartCubeMap())
	require.NoError(t, err)
	assert.Equal(t, types.MarkCell, spec.Mark)
	assert.Equal(t, types.StackDiverging, spec.Stack)
	assert.Equal(t,
		&types.Reduce{Percent: types.PercentOfTotal, MinCell: stackMinCell},
		spec.Reduce,
	)

	wire, err := spec.Wire()
	require.NoError(t, err)
	assert.Contains(t, string(wire), `"mark":"cell"`)
	assert.Contains(t, string(wire), `"stack":"diverging"`)
	assert.Contains(t, string(wire),
		`"reduce":{"percent":"percent_of_total","min_cell":5}`)

	tree := encodeSpec(t, &spec)
	got, err := chartspec.Decode(tree.Root, lookup(tree))
	require.NoError(t, err)
	assert.Equal(t, spec, got)

	plain := fixtures.Build(func(row *types.ChartSpec) {
		*row = spec
		row.Stack = types.StackNone
		row.Reduce = nil
	})
	assert.NotEqual(t, tree.Root, encodeSpec(t, &plain).Root)

	for name, text := range map[string]string{
		"stack":    `"stack": "wavy"`,
		"percent":  `"reduce": {"percent": "of_nothing"}`,
		"min_cell": `"reduce": {"min_cell": -1}`,
	} {
		bad := `{"cube": "genai_calls", "rows": {"field": "team"}, ` + text + `}`
		_, err := chartspec.Parse([]byte(bad), chartCubeMap())
		require.Error(t, err, name)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err), name)
	}
}

const (
	// stackMinCell is the min_cell of cellSpecJSON.
	stackMinCell = 5
)
