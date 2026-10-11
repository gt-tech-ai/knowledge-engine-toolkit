package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
)

// TestRun_SkipCount tests the WithSkipCount context flag: a Count:true reader whose
// context carries the flag skips the bounded total_count entirely (Total 0, no runner
// Count call), while the flag stays scoped to its own call and never leaks to a sibling
// List.
//
// Why this test is important:
//   - The count-cache decorator injects a cached total and MUST suppress the store's own
//     recompute, else the cache saves nothing. But the suppression is a context flag; if
//     it leaked to an unrelated List on the same base context, that List would silently
//     return Total 0 for a real total — a correctness bug worse than a slow count. This
//     pins both the skip and its isolation.
//
// What it tests:
//   - flag set → Total 0 and the runner's Count is never called; flag unset → the bounded
//     total is computed (Count called with cap+1); a skip-count child used for one call
//     does NOT affect a second List run on the ORIGINAL context.
func TestRun_SkipCount(t *testing.T) {
	t.Parallel()

	t.Run("flag skips the count entirely", func(t *testing.T) {
		t.Parallel()
		r := &countStubRunner{rows: []int{1, 2}, total: 500}
		page, err := listquery.Run(
			listquery.WithSkipCount(context.Background()),
			types.PageRequest{},
			runCountReader(r, 10),
		)
		require.NoError(t, err)
		assert.Equal(t, int64(0), page.Total, "skip-count leaves Total 0")
		assert.False(t, page.TotalIsEstimate)
		assert.Equal(t, 0, r.gotBound, "the runner's Count was never called")
	})

	t.Run("without the flag the bounded total is computed", func(t *testing.T) {
		t.Parallel()
		r := &countStubRunner{rows: []int{1, 2}, total: 3}
		page, err := listquery.Run(
			context.Background(),
			types.PageRequest{},
			runCountReader(r, 10),
		)
		require.NoError(t, err)
		assert.Equal(t, int64(3), page.Total)
		assert.Equal(t, 11, r.gotBound, "Count was called with cap+1")
	})

	t.Run(
		"the flag does not leak to a sibling List on the original context",
		func(t *testing.T) {
			t.Parallel()
			base := context.Background()
			// A skip-count child context is used for one call...
			skipped := &countStubRunner{rows: []int{1}, total: 100}
			_, err := listquery.Run(
				listquery.WithSkipCount(base),
				types.PageRequest{},
				runCountReader(skipped, 10),
			)
			require.NoError(t, err)
			// ...and the ORIGINAL context still computes a real total for an unrelated reader.
			sibling := &countStubRunner{rows: []int{1, 2, 3}, total: 3}
			page, err := listquery.Run(
				base,
				types.PageRequest{},
				runCountReader(sibling, 10),
			)
			require.NoError(t, err)
			assert.Equal(
				t,
				int64(3),
				page.Total,
				"the sibling on the original ctx is unaffected",
			)
			assert.Equal(t, 11, sibling.gotBound)
		},
	)
}
