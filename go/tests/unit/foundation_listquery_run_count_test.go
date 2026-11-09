package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// countStubRunner is a tiny in-test double of the small consumer-side ListRunner
// interface that foundation/listquery.Run drives (the legal unit double per the two-tier
// policy — the real Ent runner is exercised by the store sqlmock + integration suites).
// Its Count reports min(total, bound), mirroring a real runner's LIMIT-bounded id fetch,
// and records the bound it was asked for.
type countStubRunner struct {
	rows     []int // rows List returns (already the page; Run trims to limit)
	total    int   // the true number of matching rows Count would tally if unbounded
	gotBound int   // the bound Run passed to Count (asserted = count_cap+1)
}

func (r *countStubRunner) List(context.Context, types.ListSpec) ([]int, error) {
	return r.rows, nil
}

func (r *countStubRunner) Count(
	_ context.Context,
	_ []types.Filter,
	bound int,
) (int, error) {
	r.gotBound = bound
	if r.total > bound {
		return bound, nil
	}

	return r.total, nil
}

// runCountReader builds an offset-arm Reader over the stub with the given count limit.
func runCountReader(r *countStubRunner, limit int) listquery.Reader[int, int] {
	return fixtures.Build(func(row *listquery.Reader[int, int]) {
		row.Runner = r
		row.ToDomain = func(i int) (int, error) { return i, nil }
		row.Resolve = listquery.IdentityColumn
		row.Sort = []types.OrderField{
			fixtures.Build(func(row *types.OrderField) {
				row.Field = textName
			}),
		}
		row.Tiebreaker = textID
		row.Bounds = listquery.Bounds{
			DefaultSize: defaultSize,
			CountCap:    limit,
			MaxSize:     0,
		}
		row.MapErr = func(err error) error { return err }
		row.Count = true
	})
}

// TestRun_OffsetTotalCount_BoundedExactVsEstimate tests the offset arm's bounded
// total_count decision: a tally within the cap is exact, a tally over the cap is reported
// AS the cap with total_is_estimate=true, and Run asks the runner for exactly
// count_cap+1.
//
// Why this test is important:
//   - total_count must never trigger a full-table COUNT on a huge set; the pager renders
//     "N+" for an over-cap set. A wrong boundary (off-by-one on the cap, or a missing
//     estimate flag) would either leak an unbounded count or mislabel an exact count as
//     an estimate.
//
// What it tests:
//   - total ≤ cap → Total = total, TotalIsEstimate = false; total > cap → Total = cap,
//     TotalIsEstimate = true; the runner is always asked for cap+1.
func TestRun_OffsetTotalCount_BoundedExactVsEstimate(t *testing.T) {
	t.Parallel()

	t.Run("within cap is exact", func(t *testing.T) {
		t.Parallel()

		currentCountStubRunner := &countStubRunner{
			rows:     []int{1, 2, 3},
			total:    3,
			gotBound: 0,
		}
		page, err := listquery.Run(
			context.Background(),
			stdx.Zero[types.PageRequest](),
			runCountReader(currentCountStubRunner, 10),
		)
		require.NoError(t, err)
		assert.Equal(t, int64(3), page.Total)
		assert.False(t, page.TotalIsEstimate, "an at-or-under-cap count is exact")
		assert.Equal(
			t,
			11,
			currentCountStubRunner.gotBound,
			"Run asks the runner for count_cap+1",
		)
	})

	t.Run("over cap is a capped estimate", func(t *testing.T) {
		t.Parallel()
		assertRunCountAtCap(t, 5000, true, "an over-cap count")
	})

	testRunOffsetTotalCountBoundedExactVsEstimateAtCap(t)
}

// assertRunCountAtCap runs a page whose true count is total against a cap of 10 and
// asserts the page reports the cap, with the estimate flag set to wantEstimate.
func assertRunCountAtCap(t *testing.T, total int, wantEstimate bool, label string) {
	t.Helper()

	runner := &countStubRunner{rows: []int{1, rows2, rows3}, total: total, gotBound: 0}
	page, err := listquery.Run(
		context.Background(),
		stdx.Zero[types.PageRequest](),
		runCountReader(runner, limit10),
	)
	require.NoError(t, err)
	assert.Equal(t, int64(int64Arg10), page.Total, label)
	assert.Equal(t, wantEstimate, page.TotalIsEstimate, label)
	assert.Equal(
		t,
		wantGotBound11,
		runner.gotBound,
		"Run asks the runner for count_cap+1",
	)
}

// testRunOffsetTotalCountBoundedExactVsEstimateAtCap continues
// TestRun_OffsetTotalCount_BoundedExactVsEstimate: at cap.
func testRunOffsetTotalCountBoundedExactVsEstimateAtCap(t *testing.T) {
	t.Helper()

	// The exact boundary — total == cap and total == cap+1 — pins the `>` in boundedTotal
	// so a `>`/`>=` off-by-one can't slip through (the below/above cases are too far to
	// catch it).
	t.Run("exactly at cap is exact", func(t *testing.T) {
		t.Parallel()
		assertRunCountAtCap(t, 10, false, "total == cap")
	})

	t.Run("one over cap is a capped estimate", func(t *testing.T) {
		t.Parallel()
		assertRunCountAtCap(t, 11, true, "total == cap+1")
	})
}

// TestRun_OffsetTotalCount_DefaultCapFallback tests that a reader that leaves
// Bounds.CountCap unset still bounds the count at the foundation default — so no store
// can accidentally ship a full-table COUNT by forgetting to wire the cap.
//
// Why this test is important:
//   - The cap is a safety property, not a per-store opt-in; a store built from consts (no
//     config) must still get a bounded count. A regression here would reintroduce the
//     unbounded COUNT the bounded-count design exists to prevent.
//
// What it tests:
//   - With Bounds.CountCap == 0, Run asks the runner for DefaultCountCap+1.
func TestRun_OffsetTotalCount_DefaultCapFallback(t *testing.T) {
	t.Parallel()

	currentCountStubRunner := &countStubRunner{rows: []int{1}, total: 1, gotBound: 0}
	_, err := listquery.Run(
		context.Background(),
		stdx.Zero[types.PageRequest](),
		runCountReader(currentCountStubRunner, 0),
	)
	require.NoError(t, err)
	assert.Equal(t, listquery.DefaultCountCap+1, currentCountStubRunner.gotBound,
		"an unset cap falls back to the foundation default")
}

const (
	// defaultSize is the default size the tests use.
	defaultSize = 50
)
