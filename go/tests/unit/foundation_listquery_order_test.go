package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// sortMap is the allow-list the ParseSort tests share: three sortable fields and one
// filter-only field.
func sortMap() *listquery.Map {
	return listquery.NewMap().Add(
		new(listquery.Time(textCreatedAt)).Sortable(),
		new(listquery.String(textName)).Sortable(),
		new(listquery.String(textStatus)).Sortable(),
		listquery.String(textSecretNote),
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

	fieldMap := sortMap()

	testParseSortParsesDirectionsCases(t, fieldMap)
}

// ascOrder returns an ascending sort on field.
func ascOrder(field string) types.OrderField {
	return fixtures.Build(func(row *types.OrderField) { row.Field = field })
}

// descOrder returns a descending sort on field.
func descOrder(field string) types.OrderField {
	return fixtures.Build(func(row *types.OrderField) {
		row.Field = field
		row.Desc = true
	})
}

// ascColumn returns an ascending keyset column.
func ascColumn(column string) listquery.KeysetColumn {
	return fixtures.Build(func(row *listquery.KeysetColumn) { row.Column = column })
}

// descColumn returns a descending keyset column.
func descColumn(column string) listquery.KeysetColumn {
	return fixtures.Build(func(row *listquery.KeysetColumn) {
		row.Column = column
		row.Desc = true
	})
}

// testParseSortParsesDirectionsCases continues TestParseSort_ParsesDirections.
func testParseSortParsesDirectionsCases(t *testing.T, fieldMap *listquery.Map) {
	t.Helper()

	cases := []struct {
		name string
		spec string
		want []types.OrderField
	}{
		{textEmpty, "", nil},
		{"blank", textSpaceSpaceSpace, nil},
		{
			"single field default asc",
			textName,
			[]types.OrderField{
				ascOrder(textName),
			},
		},
		{
			"explicit asc",
			"name:asc",
			[]types.OrderField{
				ascOrder(textName),
			},
		},
		{
			"explicit desc",
			"created_at:DESC",
			[]types.OrderField{
				descOrder(textCreatedAt),
			},
		},
		{
			"multi preserves order",
			"status,name:desc",
			[]types.OrderField{
				ascOrder(textStatus),
				descOrder(textName),
			},
		},
		{
			"whitespace tolerated",
			" name : desc , created_at ",
			[]types.OrderField{
				descOrder(textName),
				ascOrder(textCreatedAt),
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got, err := listquery.ParseSort(fieldMap, testCase.spec)
			require.NoError(t, err)
			assert.Equal(t, testCase.want, got)
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

	fieldMap := sortMap()

	for name, spec := range map[string]string{
		textUnknownField:  textBogus,
		"unsortable":      textSecretNote,
		"bad direction":   "name:sideways",
		"extra colon":     "name:asc:desc",
		"empty token":     "name,,status",
		"empty field":     ":desc",
		"duplicate field": "name,status,name:desc",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := listquery.ParseSort(fieldMap, spec)
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

	cases := testKeysetColumnsTable()
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(
				t,
				testCase.want,
				listquery.KeysetColumns(
					identity,
					nil,
					nil,
					nil,
					testCase.ofs,
					testCase.tiebreaker,
				),
			)
		})
	}
}

// testKeysetColumnsRow is one row of testKeysetColumnsTable.
type testKeysetColumnsRow struct {
	name       string
	ofs        []types.OrderField
	tiebreaker string
	want       []listquery.KeysetColumn
}

// testKeysetColumnsTable builds the table TestKeysetColumns runs.
func testKeysetColumnsTable() []testKeysetColumnsRow {
	return []testKeysetColumnsRow{
		{
			"empty sort → created_at DESC, id DESC",
			nil,
			"",
			[]listquery.KeysetColumn{
				descColumn(textCreatedAt),
				descColumn(textID),
			},
		},
		{
			"custom tiebreaker replaces created_at",
			nil,
			textJoinedAt,
			[]listquery.KeysetColumn{
				descColumn(textJoinedAt),
				descColumn(textID),
			},
		},
		{
			"caller sort precedes the DESC tiebreakers",
			[]types.OrderField{
				ascOrder(textName),
			},
			"",
			[]listquery.KeysetColumn{
				ascColumn(textName),
				descColumn(textCreatedAt),
				descColumn(textID),
			},
		},
		{
			"caller sort on the tiebreaker is not re-appended (keeps caller ASC)",
			[]types.OrderField{
				ascOrder(textCreatedAt),
			},
			"",
			[]listquery.KeysetColumn{
				ascColumn(textCreatedAt),
				descColumn(textID),
			},
		},
		{
			"caller sort on id is not re-appended (keeps caller ASC)",
			[]types.OrderField{
				ascOrder(textID),
			},
			"",
			[]listquery.KeysetColumn{
				ascColumn(textID),
				descColumn(textCreatedAt),
			},
		},
	}
}

// FuzzParseSort asserts ParseSort never panics, never returns a field outside the
// sortable allow-list, and fails only with CodeInvalidInput, over arbitrary
// comma/colon-split input — the properties that make it a safe transport-edge parser.
func FuzzParseSort(f *testing.F) {
	fieldMap := sortMap()
	sortable := map[string]bool{textCreatedAt: true, textName: true, textStatus: true}

	for _, seed := range []string{
		"", textName, "a:desc,b", "created_at:desc,name", ",,,", "x:y:z", " : ", textSecretNote,
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, spec string) {
		got, err := listquery.ParseSort(fieldMap, spec)
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

const (
	// textSecretNote is the literal "secret_note".
	textSecretNote = "secret_note"
)
