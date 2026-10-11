package unit_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
)

// filterGolden is testdata/listquery/filters.golden.json: one field schema and the
// filter cases every filter codec and parser must agree on.
type filterGolden struct {
	// SchemaFields maps each allow-listed field to its type token.
	SchemaFields map[string]string `json:"schema_fields"`

	// Cases are the filter strings and their expected outcomes.
	Cases []struct {
		// Name labels the case.
		Name string `json:"name"`

		// Input is the filter in the JSON grammar.
		Input string `json:"input"`

		// Want is the parsed filter, or the error code parsing returns.
		Want struct {
			// ErrorCode is the coded error parsing returns.
			ErrorCode apperr.ErrorCode `json:"error_code"`

			// Filter is the parsed filter in its wire shape (see filterWire).
			Filter json.RawMessage `json:"filter"`
		} `json:"want"`
	} `json:"cases"`
}

// loadFilterGolden reads the shared filter corpus and builds its allow-list.
func loadFilterGolden(tb testing.TB) (filterGolden, *listquery.Map) {
	tb.Helper()
	raw, err := os.ReadFile(
		filepath.Join("..", "..", "..", "testdata", "listquery", "filters.golden.json"),
	)
	require.NoError(tb, err)
	var g filterGolden
	require.NoError(tb, json.Unmarshal(raw, &g))
	ctors := map[string]func(string) listquery.Field{
		"string": listquery.String, "id": listquery.ID, "int": listquery.Int,
		"float": listquery.Float, "bool": listquery.Bool, "time": listquery.Time,
	}
	m := listquery.NewMap()
	for name, typ := range g.SchemaFields {
		ctor, ok := ctors[typ]
		require.True(tb, ok, "unknown field type %q", typ)
		m.Add(ctor(name))
	}
	return g, m
}

// filterWire renders a parsed filter in the corpus's wire shape: a clause as
// {field, op, value} (a time as RFC 3339), a group as {and: [...]} or {or: [...]}, and
// the empty filter as {}.
func filterWire(f types.Filter) any {
	switch v := f.(type) {
	case types.FilterClause:
		return map[string]any{
			"field": v.Field,
			"op":    string(v.Operator),
			"value": valueWire(v.Value),
		}
	case types.CompositeFilter:
		switch {
		case len(v.And) > 0:
			return map[string]any{"and": groupWire(v.And)}
		case len(v.Or) > 0:
			return map[string]any{"or": groupWire(v.Or)}
		default:
			return map[string]any{}
		}
	default:
		return nil
	}
}

// groupWire renders each member of a group.
func groupWire(members []types.Filter) []any {
	out := make([]any, 0, len(members))
	for _, m := range members {
		out = append(out, filterWire(m))
	}
	return out
}

// valueWire renders a clause value, formatting times as RFC 3339.
func valueWire(v any) any {
	switch x := v.(type) {
	case time.Time:
		return x.Format(time.RFC3339)
	case []any:
		out := make([]any, 0, len(x))
		for _, el := range x {
			out = append(out, valueWire(el))
		}
		return out
	default:
		return v
	}
}

// canonicalJSON re-encodes JSON with sorted keys and exact numbers.
func canonicalJSON(tb testing.TB, raw []byte) string {
	tb.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	require.NoError(tb, dec.Decode(&v))
	out, err := json.Marshal(v)
	require.NoError(tb, err)
	return string(out)
}

// TestListQueryGolden_ParsesCorpus tests listquery.Parse against the shared filter
// corpus.
//
// Why this test is important:
//   - The corpus is the contract every filter codec loads; a Go parser that drifts from
//     it accepts a filter another client rejects, or parses it to different rows.
//
// What it tests:
//   - each valid case parses to exactly its expected filter (field, operator, typed
//     value and grouping), and each invalid case (depth 33, an unknown field or
//     operator, a type mismatch, an integer beyond 2^53, a bad time, an empty $and)
//     fails with its expected error code.
func TestListQueryGolden_ParsesCorpus(t *testing.T) {
	t.Parallel()
	g, m := loadFilterGolden(t)
	for _, c := range g.Cases {
		got, err := listquery.Parse(m, c.Input)
		if c.Want.ErrorCode != "" {
			require.Error(t, err, c.Name)
			assert.Equal(t, c.Want.ErrorCode, apperr.Code(err), c.Name)
			continue
		}
		require.NoError(t, err, c.Name)
		gotJSON, err := json.Marshal(filterWire(got))
		require.NoError(t, err)
		assert.Equal(
			t,
			canonicalJSON(t, c.Want.Filter),
			canonicalJSON(t, gotJSON),
			c.Name,
		)
	}
}

// TestParseFilter_MatchesParse tests that parsing the filter tree gives the same result
// as parsing the JSON string it came from.
//
// Why this test is important:
//   - A chart sends its filter as a tree and a list endpoint as a string; the two are one
//     filter only if both paths accept the same inputs and build the same filter.
//
// What it tests:
//   - for every corpus case, ParseFilter over the decoded FilterNode deep-equals Parse
//     over the string, and a rejected case is rejected with the same error code either
//     way.
func TestParseFilter_MatchesParse(t *testing.T) {
	t.Parallel()
	g, m := loadFilterGolden(t)
	for _, c := range g.Cases {
		want, wantErr := listquery.Parse(m, c.Input)
		var n types.FilterNode
		got, gotErr := types.Filter(nil), json.Unmarshal([]byte(c.Input), &n)
		if gotErr == nil {
			got, gotErr = listquery.ParseFilter(m, n)
		}
		assert.Equal(t, apperr.Code(wantErr), apperr.Code(gotErr), c.Name)
		assert.Equal(t, want, got, c.Name)
	}
}

// TestParseFilter_ChecksStructuredTree tests ParseFilter on trees built in code, whose
// numbers arrive as float64 the way a structured (proto) source delivers them.
//
// Why this test is important:
//   - A structured source has no integer kind, so the same type checks must hold for a
//     float64 operand as for a JSON number, and a tree built in code skips the JSON
//     codec's own nesting and arm checks.
//
// What it tests:
//   - a whole float64 on an int field parses to an int64, while a fraction or a value
//     beyond 2^53 is CodeInvalidInput;
//   - a node with two arms, an empty member node, and nesting past MaxFilterDepth are
//     CodeInvalidInput, and the zero node is the empty filter.
func TestParseFilter_ChecksStructuredTree(t *testing.T) {
	t.Parallel()
	_, m := loadFilterGolden(t)
	clause := func(v any) types.FilterNode {
		return types.FilterNode{
			Clause: &types.FilterNodeClause{
				Field: "page_count",
				Op:    types.OpEq,
				Value: v,
			},
		}
	}

	f, err := listquery.ParseFilter(m, clause(float64(3)))
	require.NoError(t, err)
	assert.Equal(
		t,
		types.FilterClause{Field: "page_count", Operator: types.OpEq, Value: int64(3)},
		f,
	)

	f, err = listquery.ParseFilter(m, types.FilterNode{})
	require.NoError(t, err)
	assert.True(t, f.IsEmpty())

	deep := clause(float64(1))
	for range types.MaxFilterDepth + 1 {
		deep = types.FilterNode{And: []types.FilterNode{deep}}
	}
	twoArms := clause(float64(1))
	twoArms.Or = []types.FilterNode{clause(float64(2))}
	for name, n := range map[string]types.FilterNode{
		"fraction":     clause(1.5),
		"beyond 2^53":  clause(float64(1 << 53)),
		"two arms":     twoArms,
		"empty member": {And: []types.FilterNode{{}}},
		"too deep":     deep,
	} {
		_, err := listquery.ParseFilter(m, n)
		require.Error(t, err, name)
		assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), "%s: got %v", name, err)
	}
}

// FuzzFilterNode_RoundTrip tests the filter codec and both parsers on arbitrary input.
//
// Why this test is important:
//   - The string and tree forms of a filter must stay interchangeable for any input a
//     client can send, not only the corpus cases.
//
// What it tests:
//   - when a string decodes to a FilterNode, encoding it and decoding again gives an
//     equal tree, and Parse over the string equals ParseFilter over the tree, error
//     codes included.
func FuzzFilterNode_RoundTrip(f *testing.F) {
	g, m := loadFilterGolden(f)
	for _, c := range g.Cases {
		f.Add(c.Input)
	}
	f.Fuzz(func(t *testing.T, in string) {
		var n types.FilterNode
		if json.Unmarshal([]byte(in), &n) != nil {
			return
		}
		out, err := json.Marshal(n)
		if err != nil {
			t.Fatalf("decoded tree does not encode: %v", err)
		}
		var again types.FilterNode
		require.NoError(t, json.Unmarshal(out, &again))
		require.Equal(t, n, again)

		want, wantErr := listquery.Parse(m, in)
		got, gotErr := listquery.ParseFilter(m, n)
		require.Equal(t, apperr.Code(wantErr), apperr.Code(gotErr))
		require.Equal(t, want, got)
	})
}
