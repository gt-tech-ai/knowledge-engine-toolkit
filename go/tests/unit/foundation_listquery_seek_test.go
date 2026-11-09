package unit_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// TestSeekFilter_Expansion tests the keyset seek's lexicographic OR-expansion.
//
// Why this test is important:
//   - The seek predicate is what makes keyset pagination correct: it must select exactly
//     the rows strictly after the cursor in the query's total order. Expressed as a
//     types.Filter it is folded by the same backend as the user filter, so a wrong
//     operator or a missing tie term would silently dup or skip rows at every page
//     boundary. The direction (ASC → `>`, DESC → `<`) and the eq-then-recurse tie
//     structure are the correctness core.
//
// What it tests:
//   - A single ascending column yields `col > v`; a single descending column yields `col
//     < v`.
//   - Two columns yield `strict(c0) OR (eq(c0) AND strict(c1))` with per-column
//     direction.
func TestSeekFilter_Expansion(t *testing.T) {
	t.Parallel()

	t.Run("single ascending column is a strict greater-than", func(t *testing.T) {
		t.Parallel()

		filter := listquery.SeekFilter(
			[]listquery.KeysetColumn{
				fixtures.Build(func(row *listquery.KeysetColumn) {
					row.Column = textCreatedAt
				}),
			},
			[]any{time.UnixMicro(1000).UTC()},
		)
		clause, ok := filter.(types.FilterClause)
		require.True(t, ok, "a single column seek is a bare clause")
		assert.Equal(t, textCreatedAt, clause.Field)
		assert.Equal(t, types.OpGt, clause.Operator)
	})

	t.Run("single descending column is a strict less-than", func(t *testing.T) {
		t.Parallel()

		filterValue := listquery.SeekFilter(
			[]listquery.KeysetColumn{
				fixtures.Build(func(row *listquery.KeysetColumn) {
					row.Column = textCreatedAt
					row.Desc = true
				}),
			},
			[]any{time.UnixMicro(1000).UTC()},
		)
		clause, ok := filterValue.(types.FilterClause)
		require.True(t, ok)
		assert.Equal(t, types.OpLt, clause.Operator)
	})

	testSeekFilterExpansionCompositeOr(t)
}

// testSeekFilterExpansionCompositeOr continues TestSeekFilter_Expansion: composite or.
func testSeekFilterExpansionCompositeOr(t *testing.T) {
	t.Helper()

	t.Run(
		"two descending columns expand to strict OR (eq AND strict)",
		func(t *testing.T) {
			t.Parallel()

			instant := time.UnixMicro(2000).UTC()
			filter := listquery.SeekFilter(
				[]listquery.KeysetColumn{
					fixtures.Build(func(row *listquery.KeysetColumn) {
						row.Column = textCreatedAt
						row.Desc = true
					}),
					fixtures.Build(func(row *listquery.KeysetColumn) {
						row.Column = textID
						row.Desc = true
					}),
				},
				[]any{instant, "the-id"},
			)
			top, found := filter.(types.CompositeFilter)
			require.True(t, found, "a multi-column seek is a composite OR")
			require.Len(t, top.Or, 2)

			// Arm 1: strict less-than on the leading column.
			strict, found := top.Or[0].(types.FilterClause)
			require.True(t, found)
			assert.Equal(t, textCreatedAt, strict.Field)
			assert.Equal(t, types.OpLt, strict.Operator)
			assert.Equal(t, instant, strict.Value)

			// Arm 2: eq on the leading column AND the strict tie on the id column.
			tail, found := top.Or[1].(types.CompositeFilter)
			require.True(t, found)
			require.Len(t, tail.And, 2)

			eq, found := tail.And[0].(types.FilterClause)
			require.True(t, found)
			assert.Equal(t, textCreatedAt, eq.Field)
			assert.Equal(t, types.OpEq, eq.Operator)

			idClause, found := tail.And[1].(types.FilterClause)
			require.True(t, found)
			assert.Equal(t, textID, idClause.Field)
			assert.Equal(t, types.OpLt, idClause.Operator)
			assert.Equal(t, "the-id", idClause.Value)
		},
	)
}
