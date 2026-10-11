package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
)

// sortMap is the allow-list the ParseSort tests share: three sortable fields and one
// filter-only field.
func sortMap() *listquery.Map {
	return listquery.NewMap().Add(
		listquery.Time("created_at").Sortable(),
		listquery.String("name").Sortable(),
		listquery.String("status").Sortable(),
		listquery.String("secret_note"),
	)
}

// TestParseSort_ParsesDirections tests the scalar `sort` wire-spec parser on valid specs.
//
// Why this test is important:
//   - ParseSort turns an untrusted comma-separated `field[:dir]` string into the ORDER BY
//     a store compiles; a dropped field, a lost direction or a reordered key silently
//     changes the order a client paginates through.
//
// What it tests:
//   - an empty or blank spec yields nil, so the default order comes from CompileOrder's
//     keyset tiebreaker;
//   - `field:dir` parses with asc the default, either case of asc/desc is accepted, field
//     order is preserved and whitespace around tokens is tolerated.
func TestParseSort_ParsesDirections(t *testing.T) {
	t.Parallel()
	m := sortMap()

	cases := []struct {
		name string
		spec string
		want []types.OrderField
	}{
		{"empty", "", nil},
		{"blank", "   ", nil},
		{"single field default asc", "name", []types.OrderField{{Field: "name"}}},
		{"explicit asc", "name:asc", []types.OrderField{{Field: "name"}}},
		{
			"explicit desc",
			"created_at:DESC",
			[]types.OrderField{{Field: "created_at", Desc: true}},
		},
		{
			"multi preserves order",
			"status,name:desc",
			[]types.OrderField{{Field: "status"}, {Field: "name", Desc: true}},
		},
		{
			"whitespace tolerated",
			" name : desc , created_at ",
			[]types.OrderField{{Field: "name", Desc: true}, {Field: "created_at"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := listquery.ParseSort(m, tc.spec)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestParseSort_RejectsUnknownUnsortableAndBadDirection tests that ParseSort refuses a
// spec it cannot honour instead of dropping the offending token.
//
// Why this test is important:
//   - ParseSort is the SQL-safety boundary for sorting. Dropping a bad token silently
//     returns rows in an order the client did not ask for; rejecting it tells the client
//     at once, and no non-allow-listed column ever reaches an ORDER BY.
//
// What it tests:
//   - an unknown field, a filter-only (unsortable) field, a bad direction, an extra
//     colon, an empty token and a repeated field each fail with CodeInvalidInput.
func TestParseSort_RejectsUnknownUnsortableAndBadDirection(t *testing.T) {
	t.Parallel()
	m := sortMap()

	for name, spec := range map[string]string{
		"unknown field":   "bogus",
		"unsortable":      "secret_note",
		"bad direction":   "name:sideways",
		"extra colon":     "name:asc:desc",
		"empty token":     "name,,status",
		"empty field":     ":desc",
		"duplicate field": "name,status,name:desc",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := listquery.ParseSort(m, spec)
			require.Error(t, err)
			assert.Nil(t, got)
			assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
		})
	}
}

// TestKeysetColumns tests the shared ordered-column sequence that both CompileOrder
// (ORDER BY) and the keyset seek compiler (WHERE) build from.
//
// Why this test is important:
//   - This sequence is the SINGLE source of the total order; if the tiebreaker column or
//     id were emitted with the wrong direction (they are DESC, not ASC) or the dedup
//     dropped a level, the ORDER BY and the keyset seek predicate would disagree and
//     every keyset walk would dup/skip. Pinning it here is what lets the seek compiler
//     mirror the ORDER BY by construction.
//
// What it tests:
//   - an empty sort yields `created_at DESC, id DESC`; a custom tiebreaker replaces
//     created_at; caller sort fields precede the tiebreakers in their own directions; and
//     a caller sort on the tiebreaker column or id is not re-appended (dedup keeps the
//     caller's direction).
func TestKeysetColumns(t *testing.T) {
	t.Parallel()
	identity := func(f string) string { return f }

	cases := []struct {
		name       string
		ofs        []types.OrderField
		tiebreaker string
		want       []listquery.KeysetColumn
	}{
		{
			"empty sort → created_at DESC, id DESC",
			nil,
			"",
			[]listquery.KeysetColumn{
				{Column: "created_at", Desc: true},
				{Column: "id", Desc: true},
			},
		},
		{
			"custom tiebreaker replaces created_at",
			nil,
			"joined_at",
			[]listquery.KeysetColumn{
				{Column: "joined_at", Desc: true},
				{Column: "id", Desc: true},
			},
		},
		{
			"caller sort precedes the DESC tiebreakers",
			[]types.OrderField{{Field: "name"}},
			"",
			[]listquery.KeysetColumn{
				{Column: "name", Desc: false},
				{Column: "created_at", Desc: true},
				{Column: "id", Desc: true},
			},
		},
		{
			"caller sort on the tiebreaker is not re-appended (keeps caller ASC)",
			[]types.OrderField{{Field: "created_at", Desc: false}},
			"",
			[]listquery.KeysetColumn{
				{Column: "created_at", Desc: false},
				{Column: "id", Desc: true},
			},
		},
		{
			"caller sort on id is not re-appended (keeps caller ASC)",
			[]types.OrderField{{Field: "id", Desc: false}},
			"",
			[]listquery.KeysetColumn{
				{Column: "id", Desc: false},
				{Column: "created_at", Desc: true},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(
				t,
				tc.want,
				listquery.KeysetColumns(identity, nil, nil, nil, tc.ofs, tc.tiebreaker),
			)
		})
	}
}

// FuzzParseSort asserts ParseSort never panics, never returns a field outside the
// sortable allow-list, and fails only with CodeInvalidInput, over arbitrary
// comma/colon-split input — the properties that make it a safe transport-edge parser.
func FuzzParseSort(f *testing.F) {
	m := sortMap()
	sortable := map[string]bool{"created_at": true, "name": true, "status": true}
	for _, seed := range []string{
		"", "name", "a:desc,b", "created_at:desc,name", ",,,", "x:y:z", " : ", "secret_note",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, spec string) {
		got, err := listquery.ParseSort(m, spec)
		if err != nil {
			assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
			assert.Nil(t, got)
			return
		}
		for _, of := range got {
			assert.True(
				t,
				sortable[of.Field],
				"returned field %q must be sortable",
				of.Field,
			)
		}
	})
}
