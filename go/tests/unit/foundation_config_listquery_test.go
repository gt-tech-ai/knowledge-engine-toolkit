package unit_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	listquerycfg "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/listquery"
)

// TestListqueryFacetConfig tests the value-suggestion facet config defaults + validation.
//
// Why this test is important:
//   - The facet row_threshold decides when a suggestion is served from the maintained
//     distinct_values table vs a live DISTINCT, and the reconcile_interval bounds the
//
// facet's staleness window. A zero/negative value would either disable the
//
//	facet silently or (a non-positive interval) stall the reconcile that is the only
//	path removing rename-orphaned values — so Validate must reject them at load time.
//
// What it tests:
//   - DefaultConfig yields the documented defaults (100k threshold, hourly reconcile) and
//     validates; a non-positive threshold or interval is rejected.
func TestListqueryFacetConfig(t *testing.T) {
	t.Parallel()

	def := listquerycfg.DefaultConfig()
	assert.Equal(t, 100000, def.Facet.RowThreshold)
	assert.Equal(t, time.Hour, def.Facet.ReconcileInterval)
	require.NoError(t, def.Validate(), "the default facet config must validate")

	badThreshold := listquerycfg.DefaultConfig()
	badThreshold.Facet.RowThreshold = 0
	require.Error(
		t,
		badThreshold.Validate(),
		"a non-positive row_threshold must be rejected",
	)

	badInterval := listquerycfg.DefaultConfig()
	badInterval.Facet.ReconcileInterval = 0
	assert.Error(
		t,
		badInterval.Validate(),
		"a non-positive reconcile_interval must be rejected",
	)
}
