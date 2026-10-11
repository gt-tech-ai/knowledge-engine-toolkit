package unit_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
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
		f := listquery.SeekFilter(
			[]listquery.KeysetColumn{{Column: "created_at", Desc: false}},
			[]any{time.UnixMicro(1000).UTC()},
		)
		clause, ok := f.(types.FilterClause)
		require.True(t, ok, "a single column seek is a bare clause")
		assert.Equal(t, "created_at", clause.Field)
		assert.Equal(t, types.OpGt, clause.Operator)
	})

	t.Run("single descending column is a strict less-than", func(t *testing.T) {
		t.Parallel()
		f := listquery.SeekFilter(
			[]listquery.KeysetColumn{{Column: "created_at", Desc: true}},
			[]any{time.UnixMicro(1000).UTC()},
		)
		clause, ok := f.(types.FilterClause)
		require.True(t, ok)
		assert.Equal(t, types.OpLt, clause.Operator)
	})

	t.Run(
		"two descending columns expand to strict OR (eq AND strict)",
		func(t *testing.T) {
			t.Parallel()
			ts := time.UnixMicro(2000).UTC()
			f := listquery.SeekFilter(
				[]listquery.KeysetColumn{
					{Column: "created_at", Desc: true},
					{Column: "id", Desc: true},
				},
				[]any{ts, "the-id"},
			)
			top, ok := f.(types.CompositeFilter)
			require.True(t, ok, "a multi-column seek is a composite OR")
			require.Len(t, top.Or, 2)

			// Arm 1: strict less-than on the leading column.
			strict, ok := top.Or[0].(types.FilterClause)
			require.True(t, ok)
			assert.Equal(t, "created_at", strict.Field)
			assert.Equal(t, types.OpLt, strict.Operator)
			assert.Equal(t, ts, strict.Value)

			// Arm 2: eq on the leading column AND the strict tie on the id column.
			tail, ok := top.Or[1].(types.CompositeFilter)
			require.True(t, ok)
			require.Len(t, tail.And, 2)
			eq, ok := tail.And[0].(types.FilterClause)
			require.True(t, ok)
			assert.Equal(t, "created_at", eq.Field)
			assert.Equal(t, types.OpEq, eq.Operator)
			idClause, ok := tail.And[1].(types.FilterClause)
			require.True(t, ok)
			assert.Equal(t, "id", idClause.Field)
			assert.Equal(t, types.OpLt, idClause.Operator)
			assert.Equal(t, "the-id", idClause.Value)
		},
	)
}
