package unit_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
)

// TestGetMap tests that GetMap returns the nested map for a present map-typed
// key and the nil default for a missing key, a wrong-typed value, or a nil map.
//
// Why this test is important:
//   - The OpenAPI post-processor walks an untyped decoded-JSON tree and gates
//     every hop on GetMap returning nil for an unexpected shape; if a wrong type
//     leaked through as a non-nil map the walk would panic or emit a malformed
//     envelope instead of falling through to the default.
//
// What it tests:
//   - present map → that map; missing key → nil; string value → nil; nil map → nil.
func TestGetMap(t *testing.T) {
	t.Parallel()

	nested := map[string]any{textA: 1}
	m := map[string]any{textObj: nested, "str": textX}

	require.Equal(t, nested, stdx.GetMap(m, textObj), "present map key returns the map")
	require.Nil(t, stdx.GetMap(m, textMissing), "absent key returns nil")
	require.Nil(t, stdx.GetMap(m, "str"), "non-map value returns nil")
	require.Nil(t, stdx.GetMap(nil, textObj), "nil map returns nil")
}

// TestGetSlice tests that GetSlice returns the slice for a present []any key and
// the nil default for a missing key, a wrong-typed value, or a nil map.
//
// Why this test is important:
//   - Callers range over the result directly; a nil default makes "missing" and
//     "wrong type" a zero-iteration no-op rather than a type-assertion panic.
//
// What it tests:
//   - present slice → that slice; missing key → nil; map value → nil; nil map → nil.
func TestGetSlice(t *testing.T) {
	t.Parallel()

	items := []any{textX, "y"}
	lookup2 := map[string]any{textList: items, textObj: map[string]any{}}

	require.Equal(
		t,
		items,
		stdx.GetSlice(lookup2, textList),
		"present slice key returns the slice",
	)
	require.Nil(t, stdx.GetSlice(lookup2, textMissing), "absent key returns nil")
	require.Nil(t, stdx.GetSlice(lookup2, textObj), "non-slice value returns nil")
	require.Nil(t, stdx.GetSlice(nil, textList), "nil map returns nil")
}

// TestGetString tests that GetString returns the string for a present
// string-typed key and the empty default for a missing key, a wrong-typed
// value, or a nil map.
//
// Why this test is important:
//   - $ref resolution keys off GetString; an empty default is the sentinel the
//     caller checks ("" → skip), so a wrong-typed value must not surface as a
//     non-empty string.
//
// What it tests:
//   - present string → that string; missing key → ""; int value → ""; nil map → "".
func TestGetString(t *testing.T) {
	t.Parallel()

	lookup2 := map[string]any{textName: textUser, textCount: textCount3}

	require.Equal(
		t,
		textUser,
		stdx.GetString(lookup2, textName),
		"present string key returns the string",
	)
	require.Empty(
		t,
		stdx.GetString(lookup2, textMissing),
		"absent key returns empty string",
	)
	require.Empty(
		t,
		stdx.GetString(lookup2, textCount),
		"non-string value returns empty string",
	)
	require.Empty(t, stdx.GetString(nil, textName), "nil map returns empty string")
}

// TestAsMap tests that AsMap asserts an arbitrary value to map[string]any,
// returning the map when it is one and nil otherwise.
//
// Why this test is important:
//   - AsMap is the value-based analogue used on range elements (path items,
//     operations) whose static type is `any`; a non-map element must become a
//     nil skip, not a panic.
//
// What it tests:
//   - map value → that map; string value → nil; nil value → nil.
func TestAsMap(t *testing.T) {
	t.Parallel()

	inner := map[string]any{textK: textV}

	require.Equal(t, inner, stdx.AsMap(any(inner)), "map value returns the map")
	require.Nil(t, stdx.AsMap(any(textX)), "non-map value returns nil")
	require.Nil(t, stdx.AsMap(nil), "nil value returns nil")
}
