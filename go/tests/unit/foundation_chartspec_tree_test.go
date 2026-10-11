package unit_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/chartspec"
)

// treeSpecJSON is a spec whose columns shelf is an operator over two measures and whose
// filter nests an $or inside an $and.
const treeSpecJSON = `{
  "cube": "genai_calls",
  "rows": {"op": "cross", "args": [{"field": "team"}, {"field": "model"}]},
  "columns": {"op": "concat", "args": [
    {"field": "tokens_in", "agg": "sum"}, {"field": "duration_s", "agg": "p95"}]},
  "filter": {"$and": [{"$eq": {"team": "t-1"}},
    {"$or": [{"$in": {"model": ["m-1", "m-2"]}}, {"$gt": {"tokens_in": 100}}]}]},
  "time_range": {"from": "2026-10-01T00:00:00Z", "to": "2026-10-09T00:00:00Z"},
  "grain": "hour"
}`

// specGolden is testdata/chartspec/specs.golden.json: full specs with their pinned root
// hash and node count.
type specGolden struct {
	// Cases are the specs and their expected trees.
	Cases []struct {
		// Name labels the case.
		Name string `json:"name"`
		// Root is the expected root hash in hex.
		Root string `json:"root"`
		// Spec is the spec in its wire form.
		Spec json.RawMessage `json:"spec"`
		// Nodes is the expected number of distinct nodes.
		Nodes int `json:"nodes"`
	} `json:"cases"`
}

// loadSpecGolden reads the spec corpus.
func loadSpecGolden(tb testing.TB) specGolden {
	tb.Helper()
	raw, err := os.ReadFile(
		filepath.Join("..", "..", "..", "testdata", "chartspec", "specs.golden.json"),
	)
	require.NoError(tb, err)
	var g specGolden
	require.NoError(tb, json.Unmarshal(raw, &g))
	return g
}

// decodeSpec decodes a spec's wire form without validating it.
func decodeSpec(tb testing.TB, raw string) types.ChartSpec {
	tb.Helper()
	var spec types.ChartSpec
	require.NoError(tb, json.Unmarshal([]byte(raw), &spec))
	return spec
}

// encodeSpec encodes a spec and fails the test on error.
func encodeSpec(tb testing.TB, spec types.ChartSpec) chartspec.Tree {
	tb.Helper()
	tree, err := chartspec.Encode(spec)
	require.NoError(tb, err)
	return tree
}

// lookup serves nodes from the given trees, the way a node store would.
func lookup(trees ...chartspec.Tree) func(chartspec.Hash) (chartspec.Node, bool) {
	return func(h chartspec.Hash) (chartspec.Node, bool) {
		for _, tree := range trees {
			if n, ok := tree.Nodes[h]; ok {
				return n, true
			}
		}
		return chartspec.Node{}, false
	}
}

// onlyIn returns the hashes of a's nodes that b does not have.
func onlyIn(a, b chartspec.Tree) map[chartspec.Hash]bool {
	out := map[chartspec.Hash]bool{}
	for h := range a.Nodes {
		if _, ok := b.Nodes[h]; !ok {
			out[h] = true
		}
	}
	return out
}

// TestEncode_LeafChangeChangesOnlyPathHashes tests that editing one leaf re-addresses
// only the nodes on its path to the root.
//
// Why this test is important:
//   - A version stores only the nodes it changed; if an aggregate change re-addressed
//     unrelated subtrees, every save would copy the whole spec and diffs would be noise.
//
// What it tests:
//   - changing one measure's sum to avg yields exactly three new nodes (the field ref,
//     its columns shelf and the root), and every other node is shared.
func TestEncode_LeafChangeChangesOnlyPathHashes(t *testing.T) {
	t.Parallel()
	before := decodeSpec(t, treeSpecJSON)
	after := decodeSpec(t, treeSpecJSON)
	after.Columns.Args[0].Field.Agg = types.AggAvg

	a, b := encodeSpec(t, before), encodeSpec(t, after)

	assert.NotEqual(t, a.Root, b.Root)
	added := onlyIn(b, a)
	assert.Len(t, added, 3)
	assert.True(t, added[b.Root])
	assert.Len(t, onlyIn(a, b), 3)
}

// TestEncode_ReorderChangesOnlyParent tests that reordering a shelf's operands
// re-addresses only that shelf and the root.
//
// Why this test is important:
//   - Order is meaning on a shelf, so a reorder must be a new version, but the reordered
//     operands themselves are unchanged and must stay shared.
//
// What it tests:
//   - swapping the two rows operands yields exactly two new nodes (the rows shelf and the
//     root), and both operand nodes keep their hashes.
func TestEncode_ReorderChangesOnlyParent(t *testing.T) {
	t.Parallel()
	before := decodeSpec(t, treeSpecJSON)
	after := decodeSpec(t, treeSpecJSON)
	after.Rows.Args[0], after.Rows.Args[1] = after.Rows.Args[1], after.Rows.Args[0]

	a, b := encodeSpec(t, before), encodeSpec(t, after)

	assert.Len(t, onlyIn(b, a), 2)
	assert.Len(t, onlyIn(a, b), 2)
	assert.Len(t, b.Nodes, len(a.Nodes))
}

// TestEncode_GoldenSpecRoots tests Encode against the pinned root hashes of the spec
// corpus.
//
// Why this test is important:
//   - Stored versions are looked up by these roots; any change to the node mapping
//     (kinds, member names, which members are omitted) re-addresses every stored spec.
//
// What it tests:
//   - each corpus spec encodes to its pinned root hash and distinct-node count, and the
//     root is one of the tree's nodes.
func TestEncode_GoldenSpecRoots(t *testing.T) {
	t.Parallel()
	g := loadSpecGolden(t)
	require.GreaterOrEqual(t, len(g.Cases), 3)
	for _, c := range g.Cases {
		tree := encodeSpec(t, decodeSpec(t, string(c.Spec)))
		assert.Equal(t, c.Root, tree.Root.String(), c.Name)
		assert.Len(t, tree.Nodes, c.Nodes, c.Name)
		assert.Contains(t, tree.Nodes, tree.Root, c.Name)
	}
}

// TestDecode_RoundTripsEncode tests that Decode rebuilds exactly the spec Encode stored.
//
// Why this test is important:
//   - Opening a saved version must give back the spec that was saved, down to operand
//     order, filter values of false or "" and the time range.
//
// What it tests:
//   - for every corpus spec and the nested-filter spec, Decode of the encoded root
//     deep-equals the decoded wire spec.
func TestDecode_RoundTripsEncode(t *testing.T) {
	t.Parallel()
	cases := loadSpecGolden(t).Cases
	inputs := make([]string, 0, len(cases)+2)
	inputs = append(inputs, treeSpecJSON, validSpecJSON)
	for _, c := range cases {
		inputs = append(inputs, string(c.Spec))
	}
	for _, in := range inputs {
		want := decodeSpec(t, in)
		tree := encodeSpec(t, want)
		got, err := chartspec.Decode(tree.Root, lookup(tree))
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
}

// TestDecode_MissingNodeIsNotFound tests Decode on an incomplete or corrupted store.
//
// Why this test is important:
//   - A store that lost a node, holds a node of an unknown kind or returns the wrong
//     bytes for a hash must fail loudly with a distinct code, not rebuild a wrong spec.
//
// What it tests:
//   - a missing child is CodeNotFound, an unknown kind is CodeInvalidInput, and a node
//     whose content does not hash to the requested address is CodeInternal.
func TestDecode_MissingNodeIsNotFound(t *testing.T) {
	t.Parallel()
	tree := encodeSpec(t, decodeSpec(t, treeSpecJSON))
	root := tree.Nodes[tree.Root]

	missing := chartspec.Tree{Nodes: map[chartspec.Hash]chartspec.Node{tree.Root: root}}
	_, err := chartspec.Decode(tree.Root, lookup(missing))
	assert.True(t, apperr.Is(err, apperr.CodeNotFound), "missing: %v", err)

	bogus := chartspec.Node{Kind: "bogus"}
	bogusHash, err := bogus.Hash()
	require.NoError(t, err)
	_, err = chartspec.Decode(bogusHash, lookup(chartspec.Tree{
		Nodes: map[chartspec.Hash]chartspec.Node{bogusHash: bogus},
	}))
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), "unknown kind: %v", err)

	_, err = chartspec.Decode(tree.Root, func(chartspec.Hash) (chartspec.Node, bool) {
		return bogus, true
	})
	assert.True(t, apperr.Is(err, apperr.CodeInternal), "tampered: %v", err)
}

// TestDiff_ReportsOnlyChangedPaths tests Diff between two versions of a spec.
//
// Why this test is important:
//   - The version history shows what changed between two saves; a diff that reports
//     whole subtrees, or misses a nested change, is unusable for review.
//
// What it tests:
//   - one aggregate change is exactly one Change at columns.args[0].agg, carrying the
//     field ref's hashes before and after;
//   - one filter operand change is exactly one Change at
//     filter.members[1].members[0].value;
//   - a removed shelf is one Change at its path with a zero After hash.
func TestDiff_ReportsOnlyChangedPaths(t *testing.T) {
	t.Parallel()
	base := decodeSpec(t, treeSpecJSON)
	a := encodeSpec(t, base)

	aggSpec := decodeSpec(t, treeSpecJSON)
	aggSpec.Columns.Args[0].Field.Agg = types.AggAvg
	b := encodeSpec(t, aggSpec)
	changes, err := chartspec.Diff(a.Root, b.Root, lookup(a, b))
	require.NoError(t, err)
	require.Len(t, changes, 1)
	assert.Equal(t, "columns.args[0].agg", changes[0].Path)
	assert.NotEqual(t, changes[0].Before, changes[0].After)
	assert.Contains(t, a.Nodes, changes[0].Before)
	assert.Contains(t, b.Nodes, changes[0].After)

	filterSpec := decodeSpec(t, treeSpecJSON)
	filterSpec.FilterTree.And[1].Or[0].Clause.Value = []any{"m-1", "m-3"}
	c := encodeSpec(t, filterSpec)
	changes, err = chartspec.Diff(a.Root, c.Root, lookup(a, c))
	require.NoError(t, err)
	require.Len(t, changes, 1)
	assert.Equal(t, "filter.members[1].members[0].value", changes[0].Path)

	noRows := decodeSpec(t, treeSpecJSON)
	noRows.Rows = nil
	d := encodeSpec(t, noRows)
	changes, err = chartspec.Diff(a.Root, d.Root, lookup(a, d))
	require.NoError(t, err)
	require.Len(t, changes, 1)
	assert.Equal(t, "rows", changes[0].Path)
	assert.True(t, changes[0].After.IsZero())
}

// FuzzEncode_RoundTrip tests Encode and Decode on arbitrary wire specs.
//
// Why this test is important:
//   - A stored version is found by its root hash; if a decoded spec re-encoded to a
//     different root, or two encodings of one spec disagreed, saves would fork history
//     and equal specs would stop sharing nodes.
//
// What it tests:
//   - for every spec Encode accepts, encoding is deterministic, Decode of the root
//     succeeds and re-encodes to the same root, and the spec's own JSON round trip
//     encodes to the same root.
func FuzzEncode_RoundTrip(f *testing.F) {
	f.Add([]byte(treeSpecJSON))
	f.Add([]byte(validSpecJSON))
	for _, c := range loadSpecGolden(f).Cases {
		f.Add([]byte(c.Spec))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		var spec types.ChartSpec
		if json.Unmarshal(raw, &spec) != nil {
			return
		}
		tree, err := chartspec.Encode(spec)
		if err != nil {
			require.True(t, apperr.Is(err, apperr.CodeInvalidInput), "%v", err)
			return
		}
		again := encodeSpec(t, spec)
		require.Equal(t, tree.Root, again.Root, "encoding is not deterministic")

		decoded, err := chartspec.Decode(tree.Root, lookup(tree))
		require.NoError(t, err)
		require.Equal(
			t,
			tree.Root,
			encodeSpec(t, decoded).Root,
			"decode changed the spec",
		)

		wire, err := json.Marshal(spec)
		require.NoError(t, err)
		require.Equal(t, tree.Root, encodeSpec(t, decodeSpec(t, string(wire))).Root,
			"the JSON round trip changed the spec")
	})
}

// TestDiff_EqualRootsIsEmpty tests Diff of a version against itself.
//
// Why this test is important:
//   - Saving an unchanged spec must read as "no changes", and the diff must not walk the
//     tree to find that out.
//
// What it tests:
//   - Diff of equal roots returns no changes without fetching a single node.
func TestDiff_EqualRootsIsEmpty(t *testing.T) {
	t.Parallel()
	tree := encodeSpec(t, decodeSpec(t, treeSpecJSON))
	changes, err := chartspec.Diff(
		tree.Root,
		tree.Root,
		func(chartspec.Hash) (chartspec.Node, bool) {
			t.Fatal("Diff fetched a node for equal roots")
			return chartspec.Node{}, false
		},
	)
	require.NoError(t, err)
	assert.Empty(t, changes)
}
