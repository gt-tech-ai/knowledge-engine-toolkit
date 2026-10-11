package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
)

// TestParseList_SortGrammar tests the scalar `sort` wire-spec parser (Set.ParseList).
//
// Why this test is important:
//   - ParseList is the SQL-safety boundary for sorting: it turns an untrusted
//     comma-separated `field[:dir]` string into allow-listed OrderFields. A bug that let
//     a non-allow-listed field through, or dropped the deterministic default, would
//     either enable arbitrary-column ORDER BY or break stable pagination. It must never
//     error (fail-safe to the default).
//
// What it tests:
//   - Empty / all-dropped specs yield an empty result (NO fixed fallback field) — the
//     deterministic default order is CompileOrder's keyset tiebreaker, so a
//     joined_at-keyed resource is never forced to ORDER BY a non-existent created_at
//     column.
//   - `field:dir` parses (default asc), order is preserved, whitespace tolerated.
//   - A non-allow-listed field is dropped (not defaulted per-token).
func TestParseList_SortGrammar(t *testing.T) {
	t.Parallel()
	set := listquery.NewSet("created_at", "name", "status")

	cases := []struct {
		name string
		spec string
		want []types.OrderField
	}{
		{"empty → empty (default comes from the keyset tiebreaker)", "", nil},
		{"single field default asc", "name", []types.OrderField{{Field: "name"}}},
		{
			"explicit desc",
			"created_at:desc",
			[]types.OrderField{{Field: "created_at", Desc: true}},
		},
		{
			"multi preserves order",
			"status,name:desc",
			[]types.OrderField{{Field: "status"}, {Field: "name", Desc: true}},
		},
		{"non-allow-listed dropped", "bogus,name", []types.OrderField{{Field: "name"}}},
		{"all-dropped → empty", "bogus,nope", nil},
		{
			"whitespace tolerated",
			" name : desc , created_at ",
			[]types.OrderField{{Field: "name", Desc: true}, {Field: "created_at"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, set.ParseList(tc.spec))
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

// FuzzParseList asserts ParseList never panics and never returns a non-allow-listed
// field, over arbitrary comma/colon-split input — the property that makes it a safe
// transport-edge parser.
func FuzzParseList(f *testing.F) {
	set := listquery.NewSet("created_at", "name", "status")
	allowed := map[string]bool{"created_at": true, "name": true, "status": true}
	for _, seed := range []string{
		"", "name", "a:desc,b", "created_at:desc,name", ",,,", "x:y:z", " : ",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, spec string) {
		got := set.ParseList(spec)
		// The invariant is safety, not non-emptiness: ParseList may return empty (the default
		// order then comes from CompileOrder's keyset tiebreaker), but it must NEVER return a
		// field outside the allow-list.
		for _, of := range got {
			assert.True(
				t,
				allowed[of.Field],
				"returned field %q must be allow-listed",
				of.Field,
			)
		}
	})
}
