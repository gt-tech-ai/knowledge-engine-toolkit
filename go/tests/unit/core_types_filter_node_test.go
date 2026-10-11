package unit_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// TestFilterNode_JSONRoundTripsGrammar tests that the FilterNode codec reads and writes
// the JSON filter grammar listquery.Parse accepts, without changing a byte.
//
// Why this test is important:
//   - A list endpoint receives the filter as a GET string and a chart query as a tree;
//     both are one filter only if the codec maps one onto the other losslessly. A codec
//     that reorders keys or turns an integer into a float changes a saved filter.
//
// What it tests:
//   - every grammar input (each operator, $in arrays including an empty one, nested
//     $and/$or, a large integer, and the empty object) decodes and re-encodes to the
//     compacted input exactly;
//   - a decoded clause carries its field, operator, and a json.Number for a number.
func TestFilterNode_JSONRoundTripsGrammar(t *testing.T) {
	t.Parallel()

	inputs := []string{
		`{}`,
		`{"$eq": {"status": "indexed"}}`,
		`{"$ne": {"status": "failed"}}`,
		`{"$in": {"status": ["indexed", "failed"]}}`,
		`{"$not_in": {"status": []}}`,
		`{"$like": {"name": "report"}}`,
		`{"$gt": {"created_at": "2026-08-24T12:00:00Z"}}`,
		`{"$gte": {"score": 0.75}}`,
		`{"$lt": {"page_count": 9007199254740993}}`,
		`{"$lte": {"active": true}}`,
		`{"$and": [ {"$eq": {"status": "indexed"}}, {"$like": {"name": "report"}} ]}`,
		`{"$or": [ {"$eq": {"status": "indexed"}}, {"$and": [{"$eq": {"name": "a"}}]} ]}`,
		`{"$and": []}`,
	}
	for _, in := range inputs {
		var n types.FilterNode
		require.NoError(t, json.Unmarshal([]byte(in), &n), in)
		out, err := json.Marshal(n)
		require.NoError(t, err, in)
		var want bytes.Buffer
		require.NoError(t, json.Compact(&want, []byte(in)))
		assert.Equal(t, want.String(), string(out), in)
	}

	var n types.FilterNode
	require.NoError(t, json.Unmarshal([]byte(`{"$lt": {"page_count": 12}}`), &n))
	require.NotNil(t, n.Clause)
	assert.Equal(t, types.FilterNodeClause{
		Field: "page_count", Op: types.OpLt, Value: json.Number("12"),
	}, *n.Clause)
}

// TestFilterNode_RejectsTwoArms tests that a node setting more than one arm, or JSON
// outside the grammar, is a CodeInvalidInput error instead of a silently chosen arm.
//
// Why this test is important:
//   - A tree built in code (or mapped from proto) could set both a clause and a group;
//     encoding just one of them would drop half the filter without a trace.
//
// What it tests:
//   - marshalling a node with a clause and an $and group fails with CodeInvalidInput;
//   - decoding an unknown operator, two operator keys, a body with two fields, a
//     non-array $or, and a non-object node each fail with CodeInvalidInput.
func TestFilterNode_RejectsTwoArms(t *testing.T) {
	t.Parallel()

	both := types.FilterNode{
		Clause: &types.FilterNodeClause{Field: "name", Op: types.OpEq, Value: "a"},
		And:    []types.FilterNode{{}},
	}
	_, err := json.Marshal(both)
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), "got %v", err)

	for name, in := range map[string]string{
		"unknown operator":  `{"$regex": {"name": "x"}}`,
		"two operator keys": `{"$eq": {"name": "a"}, "$ne": {"name": "b"}}`,
		"two body fields":   `{"$eq": {"name": "a", "status": "b"}}`,
		"non-array group":   `{"$or": {"$eq": {"name": "a"}}}`,
		"non-object node":   `["$eq"]`,
	} {
		var n types.FilterNode
		err := json.NewDecoder(strings.NewReader(in)).Decode(&n)
		require.Error(t, err, name)
		assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), "%s: got %v", name, err)
	}
}
