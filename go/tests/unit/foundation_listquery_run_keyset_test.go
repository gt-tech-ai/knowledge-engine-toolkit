package unit_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
)

// keysetStubRunner is a keyset-capable in-test double of the small ListRunner interface
// (the legal unit double — the real Ent runner is covered by the store sqlmock +
// integration suites). List returns a fixed page; Count is unused here.
type keysetStubRunner struct{ rows []int }

func (r keysetStubRunner) List(
	context.Context,
	types.ListSpec,
) ([]int, error) {
	return r.rows, nil
}

func (r keysetStubRunner) Count(context.Context, []types.Filter, int) (int, error) {
	return len(r.rows), nil
}

// keysetStubTime returns a fixed instant the stub reports for the created_at keyset
// column (a test never needs a live clock — a constant keeps the minted cursor
// deterministic).
func keysetStubTime() time.Time {
	return time.Unix(1_700_000_000, 0).UTC()
}

// keysetReader builds a keyset-wired Reader over the stub with the given sort (empty =
// default order).
func keysetReader(rows []int, sort []types.OrderField) listquery.Reader[int, int] {
	return listquery.Reader[int, int]{
		Runner:     keysetStubRunner{rows: rows},
		ToDomain:   func(i int) (int, error) { return i, nil },
		Resolve:    listquery.IdentityColumn,
		Sort:       sort,
		Tiebreaker: "created_at",
		Bounds:     listquery.Bounds{DefaultSize: 2},
		MapErr:     func(err error) error { return err },
		Keyset: &listquery.Keyset[int]{
			RowValue: func(row int, col string) (any, bool) {
				switch col {
				case "created_at":
					return keysetStubTime(), true
				case "id":
					return strconv.Itoa(row), true
				case "name":
					return "n" + strconv.Itoa(row), true
				default:
					return nil, false
				}
			},
			ParseID:       func(s string) (any, error) { return s, nil },
			TimestampCols: map[string]bool{"created_at": true},
		},
	}
}

// TestRun_Keyset_OnlyDefaultOrdering tests the curated-set guard: keyset (infinite
// scroll) is served ONLY on the default ordering, because only that ordering has a
// covering composite index.
//
// Why this test is important:
//   - A keyset seek on a custom sort has no covering index → a latent full-scan +
//     filesort on a big table. The guard is what keeps an un-indexed keyset off the wire;
//     without it, sorting a list and scrolling would silently degrade to O(n) per page.
//
// What it tests:
//   - A keyset cursor presented with a custom sort → CodeInvalidInput; an offset page on
//     a custom sort hands back an OFFSET next-token (page navigation), while the
//     default-ordered offset page bridges to a KEYSET next-token (infinite scroll).
func TestRun_Keyset_OnlyDefaultOrdering(t *testing.T) {
	t.Parallel()
	customSort := []types.OrderField{{Field: "name", Desc: false}}

	t.Run("keyset cursor on a custom sort is rejected", func(t *testing.T) {
		t.Parallel()
		tok, err := listquery.EncodeToken(types.PageToken{Keyset: &types.KeysetCursor{
			SortValues:  []any{"n1"},
			ID:          "1",
			Fingerprint: []byte("x"),
		}})
		require.NoError(t, err)

		_, err = listquery.Run(
			context.Background(),
			types.PageRequest{Cursor: tok},
			keysetReader([]int{1, 2, 3}, customSort),
		)
		require.Error(t, err)
		assert.True(t, apperr.Is(err, apperr.CodeInvalidInput),
			"keyset on a custom sort → CodeInvalidInput, got %v", err)
	})

	t.Run("custom-sort offset page mints an offset next-token", func(t *testing.T) {
		t.Parallel()
		page, err := listquery.Run(
			context.Background(),
			types.PageRequest{PageSize: 2},
			keysetReader([]int{1, 2, 3}, customSort),
		)
		require.NoError(t, err)
		require.NotEmpty(t, page.NextCursor, "hasMore ⇒ a next cursor")
		decoded, err := listquery.DecodeToken(page.NextCursor)
		require.NoError(t, err)
		assert.Nil(t, decoded.Keyset, "a custom sort pages by offset, not keyset")
		assert.Equal(t, int32(2), decoded.OffsetPage, "the next offset page")
	})

	t.Run("default-order offset page bridges to a keyset next-token", func(t *testing.T) {
		t.Parallel()
		page, err := listquery.Run(
			context.Background(),
			types.PageRequest{PageSize: 2},
			keysetReader([]int{1, 2, 3}, nil), // nil sort = default ordering
		)
		require.NoError(t, err)
		require.NotEmpty(t, page.NextCursor)
		decoded, err := listquery.DecodeToken(page.NextCursor)
		require.NoError(t, err)
		require.NotNil(
			t,
			decoded.Keyset,
			"the default ordering bridges to infinite scroll",
		)
		assert.Equal(t, "2", decoded.Keyset.ID, "cursor carries the last row's id")
	})

	// The fingerprint-mismatch reject path: a live keyset cursor is bound to the (sort ⊕
	// filter) it was minted under. If the query's filter changes while the client holds
	// the cursor, seeking with the old boundary would silently dup/skip rows — so Run
	// must REJECT the cursor, not honor it.
	t.Run("keyset cursor rejected when the filter changed under it", func(t *testing.T) {
		t.Parallel()
		// Mint a valid default-order keyset cursor with NO filter.
		first, err := listquery.Run(
			context.Background(),
			types.PageRequest{PageSize: 2},
			keysetReader([]int{1, 2, 3}, nil),
		)
		require.NoError(t, err)
		require.NotEmpty(t, first.NextCursor)

		// Re-run the SAME default ordering but with a filter present — the recomputed
		// fingerprint (sort ⊕ filter) no longer matches the cursor's, so the seek is
		// rejected.
		reader := keysetReader([]int{1, 2, 3}, nil)
		reader.Filter = types.FilterClause{
			Field:    "name",
			Operator: types.OpEq,
			Value:    "changed",
		}
		_, err = listquery.Run(
			context.Background(),
			types.PageRequest{Cursor: first.NextCursor},
			reader,
		)
		require.Error(t, err)
		assert.True(
			t,
			apperr.Is(err, apperr.CodeInvalidInput),
			"a cursor whose sort⊕filter fingerprint no longer matches → CodeInvalidInput, got %v",
			err,
		)
	})
}
