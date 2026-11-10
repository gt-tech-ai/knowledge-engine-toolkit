package unit_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
)

// testFilterMap is the shared allow-list the parser tests exercise: a string, a second
// string, an int, and a time field — enough to cover scalar + slice + type-mismatch
// paths.
func testFilterMap() *listquery.Map {
	return listquery.NewMap().Add(
		listquery.String(textName),
		listquery.String(textStatus),
		listquery.Int(textPageCount),
		listquery.Time(textCreatedAt),
	)
}

// TestFilterMap_LookupAndColumn tests the per-resource filter allow-list: a field is
// found with its declared type, Column defaults to Name, WithColumn overrides it, and a
// non-member is not found.
//
// Why this test is important:
//   - The Map is the SQL-safety boundary: only allow-listed fields may be filtered, and
//     the Name→Column mapping lets a DTO field name differ from its storage column. A
//     wrong lookup would reject a valid field or admit a non-allow-listed one.
//
// What it tests:
//   - Add + Lookup returns the field with correct Type/Column; WithColumn overrides the
//     default; an unknown field returns found=false.
func TestFilterMap_LookupAndColumn(t *testing.T) {
	t.Parallel()

	fieldMap := listquery.NewMap().Add(
		listquery.String(textName),
		new(listquery.ID("owner_id")).WithColumn("owner_uuid"),
		listquery.Time(textCreatedAt),
	)

	field, found := fieldMap.Lookup(textName)
	require.True(t, found)
	assert.Equal(t, textName, field.Column, "Column defaults to Name")
	assert.Equal(t, listquery.FieldString, field.Type)

	field, found = fieldMap.Lookup("owner_id")
	require.True(t, found)
	assert.Equal(t, "owner_uuid", field.Column, "WithColumn overrides the default column")
	assert.Equal(t, listquery.FieldID, field.Type)

	_, found = fieldMap.Lookup(textSecret)
	assert.False(t, found, "a non-allow-listed field is not found")
}

// TestParse_BasicAndComposite tests that the JSON filter grammar parses into the exact
// core/types Filter contract for the basic operators and the $and/$or composites.
//
// Why this test is important:
//   - This parser is the wire→contract boundary every list endpoint depends on; a
//     mis-parsed operator, field, or value silently returns the wrong rows.
//
// What it tests:
//   - $eq/$in/$like parse to FilterClause with the right operator + value; $and/$or
//     parse to a CompositeFilter holding the sub-clauses; empty/blank → nil filter.
func TestParse_BasicAndComposite(t *testing.T) {
	t.Parallel()

	fieldMap := testFilterMap()

	// Empty / blank → an empty filter (IsEmpty; a store applies no WHERE).
	for _, blank := range []string{"", textSpaceSpaceSpace, "{}"} {
		f, err := listquery.Parse(fieldMap, blank)
		require.NoError(t, err)
		require.NotNil(t, f)
		assert.True(t, f.IsEmpty(), "blank filter %q → empty", blank)
	}

	// $eq scalar.
	filter, err := listquery.Parse(fieldMap, `{"$eq": {"status": "indexed"}}`)
	require.NoError(t, err)

	clause, found := filter.(types.FilterClause)
	require.True(t, found, "$eq → FilterClause")
	assert.Equal(t, textStatus, clause.Field)
	assert.Equal(t, types.OpEq, clause.Operator)
	assert.Equal(t, textIndexed, clause.Value)

	// $in slice.
	filter, err = listquery.Parse(fieldMap, `{"$in": {"status": ["indexed", "failed"]}}`)
	require.NoError(t, err)

	clause, found = filter.(types.FilterClause)
	require.True(t, found)
	assert.Equal(t, types.OpIn, clause.Operator)
	assert.Equal(t, []any{textIndexed, textFailed}, clause.Value)

	// $like scalar.
	filter, err = listquery.Parse(fieldMap, `{"$like": {"name": "report"}}`)
	require.NoError(t, err)

	clause = stdx.As[types.FilterClause](filter)
	assert.Equal(t, types.OpLike, clause.Operator)
	assert.Equal(t, "report", clause.Value)

	// $and composite of two clauses.
	filter, err = listquery.Parse(fieldMap,
		`{"$and": [ {"$eq": {"status": "indexed"}}, {"$like": {"name": "report"}} ]}`)
	require.NoError(t, err)

	comp, found := filter.(types.CompositeFilter)
	require.True(t, found, "$and → CompositeFilter")
	require.Len(t, comp.And, wantAndLen)
	assert.Empty(t, comp.Or)

	first := stdx.As[types.FilterClause](comp.And[0])
	assert.Equal(t, textStatus, first.Field)

	// $or composite.
	filter, err = listquery.Parse(fieldMap,
		`{"$or": [ {"$eq": {"status": "indexed"}}, {"$eq": {"status": "failed"}} ]}`)
	require.NoError(t, err)

	comp, found = filter.(types.CompositeFilter)
	require.True(t, found)
	require.Len(t, comp.Or, wantOrLen)
	assert.Empty(t, comp.And)
}

// TestParse_RejectsUnknownFieldAndTypeMismatch tests that the parser rejects
// non-allow-listed fields, type-incompatible values, unknown operators, and malformed
// JSON with a CodeInvalidInput error rather than a silent fallback.
//
// Why this test is important:
//   - The allow-list + type check is the SQL-safety boundary; a filter on an
//     unknown/mis-typed field must be a loud client error, never a silent pass-through
//     that could reach SQL or drop the constraint.
//
// What it tests:
//   - unknown field, string-for-int type mismatch, unknown $op, and malformed JSON each
//     return a CodeInvalidInput error.
func TestParse_RejectsUnknownFieldAndTypeMismatch(t *testing.T) {
	t.Parallel()

	fieldMap := testFilterMap()

	cases := map[string]string{
		textUnknownField:    `{"$eq": {"secret": "x"}}`,
		"type mismatch":     `{"$eq": {"page_count": "not-a-number"}}`,
		textUnknownOperator: `{"$regex": {"name": "x"}}`,
		"malformed json":    `{"$eq": {"name": `,
		"two operator keys": `{"$eq": {"name": "a"}, "$ne": {"name": "b"}}`,
	}
	for name, filter := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := listquery.Parse(fieldMap, filter)
			require.Error(t, err, "must reject %q", filter)
			assert.True(t, apperr.Is(err, apperr.CodeInvalidInput),
				"want CodeInvalidInput, got %v", err)
		})
	}
}

// TestParse_InEmptyAndMixedArray tests the $in array boundary: an empty array is a valid
// (matches-nothing) clause, while a mixed-type array is rejected.
//
// Why this test is important:
//   - $in: [] and mixed-type arrays are exactly the edges the Ent compiler and
//     adversarial clients will hit; leaving them implicit risks a surprising default or a
//     type-unsafe value reaching SQL.
//
// What it tests:
//   - $in: [] → valid FilterClause with an empty slice value; a String-for-Int element in
//     an $in array → CodeInvalidInput.
func TestParse_InEmptyAndMixedArray(t *testing.T) {
	t.Parallel()

	fieldMap := testFilterMap()

	f, err := listquery.Parse(fieldMap, `{"$in": {"status": []}}`)
	require.NoError(t, err, "$in: [] is valid (matches nothing)")

	clause, ok := f.(types.FilterClause)
	require.True(t, ok)
	assert.Equal(t, types.OpIn, clause.Operator)
	assert.Equal(t, []any{}, clause.Value)

	_, err = listquery.Parse(fieldMap, `{"$in": {"page_count": [1, "two"]}}`)
	require.Error(t, err, "mixed-type $in array is rejected")
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput))
}

// TestParse_RejectsOverDepth tests that a filter nested past the depth cap is rejected
// cleanly rather than causing unbounded recursion.
//
// Why this test is important:
//   - A crafted deeply-nested $and/$or would otherwise recurse without bound and can
//     stack-overflow-panic (uncatchable by recover, crashing the process). The depth cap
//     turns that into an ordinary CodeInvalidInput.
//
// What it tests:
//   - A filter nested well past the cap returns CodeInvalidInput and does not panic.
func TestParse_RejectsOverDepth(t *testing.T) {
	t.Parallel()

	fieldMap := testFilterMap()

	// Build {"$and":[{"$and":[ ... {"$eq":{"status":"x"}} ... ]}]} nested 200 deep.
	nested := `{"$eq": {"status": "x"}}`
	for range iterationsDepth {
		nested = `{"$and": [` + nested + `]}`
	}

	_, err := listquery.Parse(fieldMap, nested)
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput),
		"deeply-nested filter → CodeInvalidInput, got %v", err)
}

// FuzzParse tests that the parser never panics and always returns either a
// CodeInvalidInput-class error or a valid Filter, for arbitrary JSON input.
//
// Why this test is important:
//   - Parsers are the classic panic/DoS surface; the SDK's safety guarantee is that any
//     client string is either a clean error or a valid Filter — never a crash. (Go can't
//     fuzz a struct, so the Map is fixed and only jsonStr is fuzzed.)
//
// What it tests:
//   - Parse(fixedMap, jsonStr) never panics; on nil error the result is a valid Filter.
func FuzzParse(f *testing.F) {
	fieldMap := testFilterMap()

	for _, seed := range []string{
		"", "{}", `{"$eq": {"status": "indexed"}}`,
		`{"$in": {"status": []}}`,
		`{"$and": [{"$eq": {"name": "a"}}, {"$or": [{"$eq": {"status": "b"}}]}]}`,
		`{"$regex": {"x": 1}}`, `not json`, `{`,
		strings.Repeat(`{"$and":[`, repeatInputParse) + `{"$eq":{"name":"x"}}` +
			strings.Repeat(`]}`, repeatInputParse),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(_ *testing.T, jsonStr string) {
		got, err := listquery.Parse(fieldMap, jsonStr)
		if err == nil && got != nil {
			// A non-nil result must be a real Filter implementation.
			_ = got.IsEmpty()
			_ = fmt.Sprintf("%T", got)
		}
	})
}

// TestParse_ScalarTypes tests the per-type value decoding for every FieldType branch:
// time (RFC3339), float, bool, and id — the branches the basic $eq/$in tests don't reach.
//
// Why this test is important:
//   - decodeScalar has a branch per field type; the Time branch in particular does
//     non-trivial RFC3339 parsing. An untested branch could silently mis-decode a value
//     (or accept a malformed one) and reach the Ent compiler with the wrong Go type.
//
// What it tests:
//   - a valid RFC3339 time parses to time.Time; a non-RFC3339 time is CodeInvalidInput;
//     float, bool, and id fields decode to their natural Go values.
func TestParse_ScalarTypes(t *testing.T) {
	t.Parallel()

	fieldMap := listquery.NewMap().Add(
		listquery.Time(textCreatedAt),
		listquery.Float("score"),
		listquery.Bool(textActiveLower),
		listquery.ID("owner_id"),
	)

	// Time: valid RFC3339 → time.Time.
	filter, err := listquery.Parse(
		fieldMap,
		`{"$gt": {"created_at": "2026-08-24T12:00:00Z"}}`,
	)
	require.NoError(t, err)

	clause := asType[types.FilterClause](t, filter)
	assert.Equal(t, types.OpGt, clause.Operator)

	ts, ok := clause.Value.(time.Time)
	require.True(t, ok, "time field decodes to time.Time, got %T", clause.Value)
	assert.Equal(t, wantYear, ts.Year())

	// Time: non-RFC3339 → CodeInvalidInput.
	_, err = listquery.Parse(fieldMap, `{"$gt": {"created_at": "not-a-time"}}`)
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput))

	// Float.
	filter, err = listquery.Parse(fieldMap, `{"$gte": {"score": 0.75}}`)
	require.NoError(t, err)
	assert.InEpsilon(
		t,
		inEpsilonInputTypes,
		asType[float64](t, asType[types.FilterClause](t, filter).Value),
		inEpsilonInputScalarTypes,
	)

	// Bool.
	filter, err = listquery.Parse(fieldMap, `{"$eq": {"active": true}}`)
	require.NoError(t, err)
	assert.Equal(t, true, asType[types.FilterClause](t, filter).Value)

	// ID (UUID compared as a string).
	filter, err = listquery.Parse(
		fieldMap,
		`{"$eq": {"owner_id": "1e965110-1fbf-440d-8ef3-ef64b1ce58ab"}}`,
	)
	require.NoError(t, err)
	assert.Equal(
		t,
		"1e965110-1fbf-440d-8ef3-ef64b1ce58ab",
		asType[types.FilterClause](t, filter).Value,
	)
}

const (
	// inEpsilonInputScalarTypes is the epsilon passed to InEpsilon in
	// TestParse_ScalarTypes.
	inEpsilonInputScalarTypes = 1e-9

	// inEpsilonInputTypes is the expected passed to InEpsilon in TestParse_ScalarTypes.
	inEpsilonInputTypes = 0.75

	// iterationsDepth is the iteration count in TestParse_RejectsOverDepth.
	iterationsDepth = 200

	// repeatInputParse is the count passed to Repeat in FuzzParse.
	repeatInputParse = 100

	// wantAndLen is the expected and len.
	wantAndLen = 2

	// wantOrLen is the expected or len.
	wantOrLen = 2

	// wantYear is the expected year.
	wantYear = 2026
)
